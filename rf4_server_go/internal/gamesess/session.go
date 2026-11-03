package gamesess

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"rf4server/internal/fishing"
	"rf4server/internal/gamestore"
	"rf4server/internal/skilltree"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// 握手走到哪一步了。三条按顺序走，走完进 live。
const (
	phaseAuth   = "auth"   // 等第一段：01 00 | u32 令牌长度 | 令牌 ASCII
	phaseHermes = "hermes" // 等第二段：一整帧壳 + 明文 <hermes>...</hermes>
	phaseLive   = "live"   // 双向 RC4 已装弹，开始正常收发
)

// loginTicketFinder 是登录票的 MySQL 查询口。单独写在这里而不是塞进 gamestore.Runtime，
// 是为了不逼所有只读背包/技能的假实现都实现登录票；运行时由 *gamestore.MySQL 命中。
type loginTicketFinder interface {
	// FindToken 按 token 或 logintoken 查一张票；没查到返回 found=false。
	FindToken(ctx context.Context, key string) (gamestore.LoginTicket, bool, error)
}

// stats 一行断开日志要用的计数，字段名尽量和 Node 版那行统计对得上，两边日志好对照。
type stats struct {
	framesIn  int // 收到的数据帧条数（不含 ack 和心跳）
	framesOut int // 发出去的数据帧条数
	acksIn    int // 客户端发来的传输 ack
	acksOut   int // 我们回给客户端的传输 ack
	reqs      int // 解出来的业务请求条数
	misses    int // 命中「还没实现」的请求条数
}

// Session 一条游戏连接。一条连接一个 goroutine（Run 里从头跑到尾），
// 只有心跳那个 goroutine 会并发写 socket，所以写的时候都拿 writeMu。
type Session struct {
	srv  *Server
	id   int
	conn net.Conn

	phase string // 现在在握手的哪一步
	token string // 登录令牌；RC4 钥匙就是这个字符串本身的 ASCII 字节
	user  string // 令牌对上的账号名；对不上就是空串（照样收帧，只是没数据可回）
	uid   int    // 账号数字 id，0 = 没对上
	ip    string // 客户端 IP，只为日志
	rport string // 客户端端口，只为日志

	chunk []byte    // 握手阶段还没切完的字节（进 live 前必须清空）
	rc4in *wire.RC4 // 收方向密钥流
	rc4   *wire.RC4 // 发方向密钥流（两条互相独立，各自从头开始）
	rx    *wire.FrameReader

	inWid    uint64          // 对端最近一帧的线序号（回 ack 要用它）
	outWid   uint64          // 我方线序号，真服从 1 开始递增，所以先发再自增是错的：这里先加再用
	answered map[uint32]bool // 应答过的 call_id：同一条请求回两次客户端会当成两条物件

	// 技能状态缓存：登录时从 MySQL（或 db.json）拉一次，5/1 和以后的学习都读它、改完写回它
	skills       skilltree.State
	skillsLoaded bool   // 上面那份是不是已经填好了，别拿零值当数据用
	skillSrc     string // 这份是从哪边拉来的（MySQL / db.json），日志里要说清

	// 钓鱼状态：手上哪套钓具、这次钩着哪条鱼、抛/咬/留计数（见 internal/fishing/state.go）。
	// 咬钩定时器那个 goroutine 会写、帧循环读，锁在 State 自己里面。
	fishingState fishing.State
	// 当前地图/水域：1/12 实际回给客户端哪个 level_*，这里就记哪个。
	// 出鱼必须按它查 MySQL 名册，不能永远用启动参数里的默认水域。
	locMu  sync.RWMutex
	mapLoc string

	skillMu sync.Mutex    // 保护上面那份技能状态缓存：5/2 学习会改它，2/1 推送 goroutine 会读它
	writeMu sync.Mutex    // 保护 socket 写（心跳 goroutine 和帧循环会同时写）
	done    chan struct{} // 关掉它，心跳 goroutine 就退出
	once    sync.Once     // 保证 close 只做一次
	// 卡住预警：最近一次收到客户端请求的时刻 + 最后一条「我们没回话」的命令号。
	// 客户端等不到回执会自己干等 120 秒才报错（6/2 实机实测），那 120 秒对着控制台什么也看不出来；
	// 这里 20 秒就提醒一行「它可能在等这条的回执」，省一轮白等。只写日志，不碰协议。
	stallMu     sync.Mutex
	lastReqAt   time.Time // 最近一次收到客户端请求的时刻
	lastQuiet   string    // 最后一条我们没回话的命令号，比如 "6/2"；回了话就清空
	stallWarned bool      // 这一轮已经提醒过了，别每 3.4 秒念一遍

	// cfgHits 这次连接里客户端按类型号要了几次配置块（1/4）。纯日志计数：
	// 同一块反复要 = 它没把我们发的当缓存收下，这条线索有用。
	cfgHits map[int]int

	st stats
}

// newSession 造一条会话。依赖都从 Server 上拿，这里不自己找文件。
func newSession(s *Server, id int, conn net.Conn) *Session {
	host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())
	return &Session{
		srv:      s,
		id:       id,
		conn:     conn,
		ip:       host,
		rport:    port,
		phase:    phaseAuth,
		chunk:    nil,
		answered: map[uint32]bool{},
		cfgHits:  map[int]int{},
		done:     make(chan struct{}),
	}
}

// tag 日志前缀，长得和 Node 版一样：#7 这种
func (s *Session) tag() string { return "#" + strconv.Itoa(s.id) }

// log 写一行游戏日志（同时会 echo 到 IDEA 控制台）
func (s *Session) log(msg string) { s.srv.Deps.Log.Line("game", s.tag()+" "+msg) }

// Run 一条连接的完整生命周期：发 GUID -> 握手 -> 帧循环 -> 收尾。
// 任何一步出错都在这里变成一行中文日志，不往外抛。
func (s *Session) Run() {
	s.log("连接 " + s.ip + ":" + s.rport)
	// 握手第一步：服务器先发 36 字节明文会话 GUID。
	// Node 版发的是录制里那一份（快照）；Go 版按「不碰快照」这条规矩自己随机生成，
	// 客户端只把它当会话标识用，不校验内容 —— 实测发过去它就直接进 auth。
	if _, err := s.write(s.firstHello()); err != nil {
		s.close("发会话 GUID 失败: " + err.Error())
		return
	}
	if err := s.handshake(); err != nil {
		s.close("握手失败: " + friendlyErr(err))
		return
	}
	if err := s.loop(); err != nil {
		s.close("连接断开: " + friendlyErr(err))
		return
	}
	s.close("对端关闭")
}

// handshake 把明文的 auth 段和 hermes 段吃完，然后装弹进 live。
func (s *Session) handshake() error {
	buf := make([]byte, 32*1024)
	for s.phase != phaseLive {
		n, err := s.conn.Read(buf)
		if n > 0 {
			s.chunk = append(s.chunk, buf[:n]...)
			if perr := s.parse(); perr != nil {
				return perr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// parse 吃掉 chunk 里已经攒够的部分：auth 段 -> hermes 段 -> 装弹。
// 字节不够就原样返回 nil，等下一次 Read 再进来。
func (s *Session) parse() error {
	for {
		switch s.phase {
		case phaseAuth:
			// 布局：01 00 | u32 令牌长度 | 令牌字节
			if len(s.chunk) < 6 {
				return nil
			}
			tl := int(binaryU32(s.chunk[2:6]))
			if tl <= 0 || tl > 4096 {
				return errors.New("auth 令牌长度不对: " + strconv.Itoa(tl))
			}
			if len(s.chunk) < 6+tl {
				return nil
			}
			s.token = string(s.chunk[6 : 6+tl])
			s.chunk = s.chunk[6+tl:]
			rec, ok, source := s.lookupLoginToken(s.token)
			if ok {
				s.user, s.uid = rec.Username, rec.UserID
			}
			s.log(s.ip + " 钥匙 token=" + s.token +
				choose(ok, " 对应用户 "+s.user+"（来源 "+source+"）", "（"+source+"）"))
			s.loadSkillsAtLogin()
			s.phase = phaseHermes
		case phaseHermes:
			// 布局：u32 体长 | u8 类型 | u64 wid | payload（payload 从第 13 字节开始）
			if len(s.chunk) < 4 {
				return nil
			}
			bl := int(binaryU32(s.chunk[0:4]))
			if bl < 9 {
				return errors.New("hermes 帧长度不对: " + strconv.Itoa(bl))
			}
			if len(s.chunk) < 4+bl {
				return nil
			}
			xml := string(s.chunk[13 : 4+bl])
			s.chunk = s.chunk[4+bl:]
			s.log("hermes: " + oneLine(xml, 200))
			s.arm()
			return nil // 剩下的字节留给帧循环接着切
		default:
			return nil
		}
	}
}

// lookupLoginToken 按「MySQL login_tokens 优先，进程内令牌兜底」认游戏通道钥匙。
// 返回令牌记录、是否认到、以及这次的来源说明。MySQL 明确找到但过期时不会回退到旧内存票。
func (s *Session) lookupLoginToken(key string) (store.Token, bool, string) {
	if s.srv.Deps.Run != nil {
		if finder, ok := s.srv.Deps.Run.(loginTicketFinder); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			rec, found, err := finder.FindToken(ctx, key)
			cancel()
			if err != nil {
				s.log(">> 游戏握手登录票查 MySQL 失败，先回退进程内令牌：" + err.Error())
			} else if found {
				if rec.Expires > 0 && rec.Expires < time.Now().Unix() {
					return store.Token{}, false, "MySQL login_tokens 里这张票已过期"
				}
				return store.Token{
					Token: rec.Token, LoginToken: rec.LoginToken, Expires: rec.Expires,
					Username: rec.Username, UserID: int(rec.UserID), ClientIP: rec.IP,
				}, true, "MySQL login_tokens"
			}
		}
	}
	rec, found := s.srv.Deps.DB.Tokens().Lookup(key)
	if found {
		return rec, true, "进程内令牌（MySQL 没查到）"
	}
	return store.Token{}, false, "MySQL login_tokens 和进程内令牌都没查到"
}

// setCurrentLocation 记录当前会话实际进入的水域；空值不覆盖旧值。
func (s *Session) setCurrentLocation(loc string) {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return
	}
	s.locMu.Lock()
	s.mapLoc = loc
	s.locMu.Unlock()
}

// currentLocation 当前会话实际进入的水域；还没走过 1/12 时为空。
func (s *Session) currentLocation() string {
	s.locMu.RLock()
	defer s.locMu.RUnlock()
	return s.mapLoc
}

// loadSkillsAtLogin 认出人之后第一件事：把这个号的技能状态拉进会话缓存。
// 优先 MySQL（少爷定的「数据全在库里」），连不上才退回 db.json，
// 而且日志里一定写清用的是哪一边 —— 之前被「快照/缓存到底哪来的」坑过太多次，来源必须看得见。
func (s *Session) loadSkillsAtLogin() {
	if s.user == "" {
		s.log("没对上账号，技能先按空的处理")
		return
	}
	st, src, err := s.pullSkillState(context.Background())
	if err != nil {
		s.log("技能状态一份都拉不到，这一会按空的处理：" + err.Error())
		st, src = skilltree.State{Cats: map[int]float64{}, Nodes: map[int]int{}}, "空"
	}
	st.Level = s.level()
	s.storeSkills(st, src)
	s.log("技能从 " + src + " 拉好：" + strconv.Itoa(len(st.Cats)) + " 个大类熟练度 " +
		strconv.Itoa(len(st.Nodes)) + " 个已学节点，发放技能点 " + strconv.Itoa(st.Granted))
}

// pullSkillState 去数据源现拿一份技能状态，返回「状态 + 这份哪来的 + 错误」。
// MySQL 优先；读不到才退回 db.json（只当兜底，日志里会写清楚，别当数）。
func (s *Session) pullSkillState(ctx context.Context) (skilltree.State, string, error) {
	if s.user == "" {
		return skilltree.State{}, "", errors.New("还没对上账号，不知道读谁的技能")
	}
	if s.srv.Deps.Run != nil {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if row, err := s.srv.Deps.Run.SkillState(c, s.user); err == nil {
			return skilltree.State{Cats: row.Cats, Nodes: row.Nodes, Granted: row.Granted}, profileSrcMySQL, nil
		} else {
			s.log(">> 技能从 MySQL 读失败，先用 db.json 那份：" + err.Error())
		}
	}
	return skillStateFromRow(s.srv.Deps.DB.SkillsFor(s.user), s.level()), profileSrcDBJSON, nil
}

// storeSkills 写会话缓存。加锁是因为 2/1 推送那条 goroutine 也在读这份，
// 学完技能这边一改，那边就可能读到「一半新一半旧」的映射。
func (s *Session) storeSkills(st skilltree.State, src string) {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	if st.Cats == nil {
		st.Cats = map[int]float64{}
	}
	if st.Nodes == nil {
		st.Nodes = map[int]int{}
	}
	st.Level = s.level()
	s.skills, s.skillsLoaded, s.skillSrc = st, true, src
}

// level 这个号的角色等级。技能树帧里要写这个数，所以必须从数据源拿，不能自己编。
// MySQL 里有账号就用 MySQL 那份（那是少爷定的真值），否则退回 db.json。
func (s *Session) level() int {
	if s.srv.Deps.Run != nil && s.user != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if acct, err := s.srv.Deps.Run.Account(ctx, s.user); err == nil {
			return acct.Level
		}
	}
	if u, ok := s.srv.Deps.DB.FindUser(s.user); ok {
		return u.Level
	}
	return 0
}

// skillState 本号当前的技能状态（会话缓存那份，学完技能会当场更新）。
// 顺手把等级刷一下：升级这种事可能在会话中途发生。
func (s *Session) skillState() skilltree.State {
	s.skillMu.Lock()
	loaded, st := s.skillsLoaded, s.skills
	src := s.skillSrc
	s.skillMu.Unlock()
	if !loaded {
		s.loadSkillsAtLogin()
		s.skillMu.Lock()
		st, src = s.skills, s.skillSrc
		s.skillMu.Unlock()
	}
	if st.Cats == nil {
		st.Cats = map[int]float64{}
	}
	if st.Nodes == nil {
		st.Nodes = map[int]int{}
	}
	st.Level = s.level()
	_ = src
	return st
}

// skillStateFromRow 把 db.json 里那行技能记录转成 skilltree 要的 State。
// JSON 的键只能是字符串，所以这里要把大类号/节点号从字符串转成整数，转换只有这一处。
func skillStateFromRow(row store.SkillRow, level int) skilltree.State {
	st := skilltree.State{Cats: map[int]float64{}, Nodes: map[int]int{}, Granted: row.PointsGranted, Level: level}
	for k, v := range row.Cats {
		if cat, err := strconv.Atoi(k); err == nil {
			st.Cats[cat] = v
		}
	}
	for k, v := range row.Nodes {
		if node, err := strconv.Atoi(k); err == nil {
			st.Nodes[node] = v
		}
	}
	return st
}

// arm 装上 RC4：钥匙 = token 字符串的 ASCII 字节本身，收发各一条独立密钥流。
// 装完顺手起心跳。
func (s *Session) arm() {
	key := []byte(s.token)
	s.rc4in = wire.NewRC4(key)
	s.rc4 = wire.NewRC4(key)
	s.phase = phaseLive
	s.log("双向 RC4 已装弹（钥匙=" + strconv.Itoa(len(key)) + " 字节 ASCII）")
	go s.heartbeat()
	// 装完弹第一件事是服务器先开口（0/1 + 2/1），不是等客户端问。
	// 这一步实测过：我们不说话，客户端就一条请求都不发，界面一直停在「载入个人资料」。
	s.speakFirst()
}

// heartbeat 按真服节奏发 4 字节全零空帧。关掉 done 就停。
func (s *Session) heartbeat() {
	tick := time.NewTicker(s.srv.Config.HB)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
			s.warnIfStalled()
			if _, err := s.write(wire.ZeroFrame); err != nil {
				return
			}
		}
	}
}

// loop 帧循环：解帧 -> 回 ack -> 解信封 -> 交给路由表。
// 出错（含 io.EOF）就返回，由 Run 打那一行断开日志。
func (s *Session) loop() error {
	// 握手时多半已经顺手读到了后面几帧的开头，剩下的字节得先喂给分帧器
	s.rx = wire.NewFrameReader(&prefixReader{pre: s.chunk, c: s.conn})
	s.chunk = nil
	for {
		f, err := s.rx.Next()
		if err != nil {
			return err
		}
		if f.Zero || len(f.Payload) == 0 {
			if f.Type == 1 {
				s.st.acksIn++
			}
			continue
		}
		s.inWid = f.Wid
		if f.Type == 1 { // 客户端的传输 ack：只有计数，没有正文
			s.st.acksIn++
			continue
		}
		if _, err := s.write(wire.AckFrame(f.Wid)); err != nil {
			return err
		}
		s.st.acksOut++
		plain := s.rc4in.Crypt(f.Payload)
		env, ok := wire.ReadEnvelope(plain)
		if !ok || env.Marker != -1 {
			continue // 只有 marker=-1 才是客户端请求；-2 是它给我们的 ack，不用管
		}
		s.st.framesIn++
		s.st.reqs++
		if s.answered[env.CallID] {
			continue // 客户端重发同一条：答过一次就不要再答一次
		}
		s.serveRequest(env, plain)
	}
}

// sendFrame 发一帧数据：wid 先加再用（真服第一帧 wid=1），payload 走 RC4，帧头保持明文。
//
// 加锁的范围必须是「取号 + 加密 + 写 socket」一整段，不能只锁最后那一下写：
// RC4 是流密码，密钥流按写出顺序一路往后走。状态推送那个 goroutine 和帧循环的应答
// 一旦交错（A 加密完还没写，B 就把号取了接着加密），两边密钥流就跟对端解出来的错位，
// 客户端看到的是乱码帧。所以这儿自己拿 writeMu，直接写 conn（不再走 s.write，那会重复加锁）。
func (s *Session) sendFrame(typ byte, payload []byte) (uint64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.outWid++
	body := wire.BuildFrame(typ, s.outWid, s.cryptOut(payload))
	if _, err := s.conn.Write(body); err != nil {
		return 0, err
	}
	s.st.framesOut++
	return s.outWid, nil
}

// sendResponse 发一条应答。payload 里第 5~8 字节就是 call_id，发之前按要应答的那条请求写进去。
func (s *Session) sendResponse(payload []byte, callID uint32) error {
	_, err := s.sendFrame(0, wire.SetCallID(payload, callID))
	return err
}

// sendPush 发一条主动推送（marker=-1，末尾那个 0x01 已经由拼帧的那层带上）。
// 客户端很多面板第一眼读的是推送不是应答，所以两处都得发。
func (s *Session) sendPush(payload []byte) error {
	_, err := s.sendFrame(0, payload)
	return err
}

// cryptOut 出站加密；还没装弹（握手阶段）就原样发。
func (s *Session) cryptOut(p []byte) []byte {
	if s.rc4 == nil {
		return p
	}
	return s.rc4.Crypt(p)
}

// write 唯一的 socket 写出口：拿锁写，避免心跳和应答交错把流写乱。
func (s *Session) write(b []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.Write(b)
}

// close 收尾：停心跳、关连接、打一行和 Node 版同格式的统计。
func (s *Session) close(why string) {
	s.once.Do(func() {
		close(s.done)
		s.log("断开（" + why + "） 收" + strconv.Itoa(s.st.framesIn) + "帧/发" + strconv.Itoa(s.st.framesOut) +
			"帧 请求" + strconv.Itoa(s.st.reqs) + "条 未实现" + strconv.Itoa(s.st.misses) + "条")
		_ = s.conn.Close()
	})
}

// prefixReader 先把手里剩的字节吐出去，再接着读真连接。
// Go 的分帧器不像 Node 那样能 push 剩余字节，就用这层把两段接起来。
type prefixReader struct {
	pre []byte   // 握手时多读到的那截
	c   net.Conn // 真连接
}

// Read 实现 io.Reader
func (p *prefixReader) Read(b []byte) (int, error) {
	if len(p.pre) > 0 {
		n := copy(b, p.pre)
		p.pre = p.pre[n:]
		return n, nil
	}
	return p.c.Read(b)
}

// randomSessionGUID 造一个随机 v4 UUID（36 字节大写 ASCII，形状和真服那份一样）。
// 默认不用它：真客户端会拿首包那 36 字节对服务器身份，随机值会被它拒掉
// （见 identity.go）。留着是给 --session-guid random 做对照实验用的。
func randomSessionGUID() []byte {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源出问题就退回一串定值，至少别让握手卡住
		copy(b[:], []byte("rf4server-uid-r1"))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // 版本位：4 = 随机 UUID
	b[8] = (b[8] & 0x3f) | 0x80 // 变体位：10x
	const hexUpper = "0123456789ABCDEF"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexUpper[v>>4], hexUpper[v&0x0f])
	}
	return out
}

// binaryU32 小端读 4 字节（这一层到处要读长度，写个小助手省得每处都 import）
func binaryU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// oneLine 把多行压成一行、并按字数截断，日志才不会把 XML 摊成十几行
func oneLine(s string, max int) string {
	t := strings.Join(strings.Fields(s), " ")
	if max > 0 && len(t) > max {
		return t[:max] + "…"
	}
	return t
}

// choose 小 helper：条件成立取 a，否则取 b（日志里那句「对应用户 xxx」就是这么拼的）
func choose(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// friendlyErr 把 Go 那串英文错误压短一点，少爷看日志不用翻译
func friendlyErr(err error) string {
	if err == nil {
		return "没有错误"
	}
	if err == io.EOF {
		return "客户端把连接关了"
	}
	return err.Error()
}

// stallAfter 客户端多久没再发新请求就算「卡住了」。
// 真服的客户端等回执是 120 秒才放弃，这里提前到 20 秒提醒，纯粹为了少白等。
const stallAfter = 20 * time.Second

// noteRequest 收到一条客户端请求就记账：这一刻起重新开始计时，上一轮的提醒作废。
// 只有「最后一条我们没回话」且「客户端之后不再说话」这种组合才是卡住，所以这里把 lastQuiet 清掉。
func (s *Session) noteRequest() {
	s.stallMu.Lock()
	defer s.stallMu.Unlock()
	s.lastReqAt = time.Now()
	s.lastQuiet = ""
	s.stallWarned = false
}

// noteQuiet 这条命令我们决定不回话（真服也没回过），记下命令号备着预警用。
func (s *Session) noteQuiet(key string) {
	s.stallMu.Lock()
	defer s.stallMu.Unlock()
	s.lastQuiet = key
}

// warnIfStalled 心跳里顺手查一次：最后一条没回话、客户端又 20 秒没动静，就提醒一行。
// 只提醒一次（连着提醒刷屏），等客户端再说话就重新计。
func (s *Session) warnIfStalled() {
	s.stallMu.Lock()
	quiet, warned := s.lastQuiet, s.stallWarned
	if quiet == "" || warned || s.lastReqAt.IsZero() || time.Since(s.lastReqAt) < stallAfter {
		s.stallMu.Unlock()
		return
	}
	s.stallWarned = true
	s.stallMu.Unlock()

	s.log("⚠ 客户端 " + strconv.Itoa(int(time.Since(s.lastReqAt).Seconds())) +
		" 秒没再发新请求，最后一条没回话的是 " + quiet +
		" ｜ 它可能在等这条的回执（等不到会干等 120 秒再抛 HermesError 断开）" +
		" ｜ 修法：把它加进 internal/entry/policy.go 的 clientAwaitsReply，再在路由表挂个处理器")
}
