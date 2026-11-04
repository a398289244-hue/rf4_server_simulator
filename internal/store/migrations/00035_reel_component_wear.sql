-- 渔线轮分部件耐久。顶层 item.durability 是五项的加权综合值，
-- 旧档升级时以原轮子耐久回填，避免凭空修复已磨损的轮子。
ALTER TABLE reel_performance ADD COLUMN spool_durability REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN drag_durability REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN bearing_durability REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN oil_durability REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN gear_durability REAL NOT NULL DEFAULT 1;
ALTER TABLE reel_performance ADD COLUMN line_out_m REAL NOT NULL DEFAULT 0;
ALTER TABLE reel_performance ADD COLUMN retrieve_m REAL NOT NULL DEFAULT 0;
ALTER TABLE reel_performance ADD COLUMN drag_work REAL NOT NULL DEFAULT 0;

UPDATE reel_performance SET
    spool_durability=durability,
    drag_durability=durability,
    bearing_durability=durability,
    oil_durability=durability,
    gear_durability=durability;
