package gamestore

// 登录票。一次登录发两张：
//
//	token      给游戏 TCP 通道用，同时也是 RC4 的钥匙（32 位十六进制）
//	logintoken 给客户端本地缓存，下次启动拿它去 /login_token.php 续期（48 位十六进制）
//
// 为什么必须落 MySQL（login_tokens 表）：客户端会把 logintoken 存在本地，
// 后端一重启内存表就空了，续期查不到 → 客户端直接报「请过几分钟再试」，
// 看着像服务器挂了。票存在库里，重启多少次都接得上，而且少爷你在库里能直接看直接删。
type LoginTicket struct {
	Token      string // 游戏通道钥匙（login_tokens.token，主键）
	LoginToken string // 网页/客户端用的长票（login_tokens.logintoken）
	Username   string // 谁的账号（login_tokens.username）
	UserID     int64  // 账号 id（login_tokens.user_id）
	Expires    int64  // 到期时间，epoch 秒（login_tokens.expires_at）
	CreatedAt  int64  // 签发时间，epoch 秒（login_tokens.created_at）
	IP         string // 哪个 IP 登的（login_tokens.ip）
	Version    string // 客户端报的版本（login_tokens.version）
	MachineID  string // 机器码（login_tokens.machine_id）
}
