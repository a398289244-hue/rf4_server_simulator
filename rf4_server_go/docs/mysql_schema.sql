-- RF4 MySQL runtime schema
-- Generated from the development database. No player data is included.
-- Scope: login, profile, inventory, keepnet, fishing, shop, rig, shortcut and skills.

-- container_items
CREATE TABLE IF NOT EXISTS `container_items` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `username` varchar(32) NOT NULL,
  `container` varchar(32) NOT NULL,
  `pos` int NOT NULL DEFAULT '0',
  `item_key` varchar(64) NOT NULL DEFAULT '',
  `guid` char(32) DEFAULT NULL,
  `price` bigint NOT NULL DEFAULT '0',
  `head` varchar(16) NOT NULL DEFAULT '',
  `cond_bits` varchar(8) NOT NULL DEFAULT '',
  `n1_bits` varchar(8) NOT NULL DEFAULT '',
  `cond` double NOT NULL DEFAULT '1',
  `n1` int NOT NULL DEFAULT '0',
  `tag` varchar(16) NOT NULL DEFAULT '',
  `args` json DEFAULT NULL,
  `raw_hex` text,
  `source` varchar(64) NOT NULL DEFAULT '',
  `created_at` bigint NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`),
  KEY `idx_ci_user` (`username`,`container`),
  KEY `idx_ci_guid` (`guid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- containers
CREATE TABLE IF NOT EXISTS `containers` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `username` varchar(32) NOT NULL,
  `container` varchar(32) NOT NULL,
  `tag` int DEFAULT NULL,
  `field` varchar(16) NOT NULL DEFAULT '',
  `elem_tag` int DEFAULT NULL,
  `pattern` json DEFAULT NULL,
  `seeded_from` varchar(191) NOT NULL DEFAULT '',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_container` (`username`,`container`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- fish_records
CREATE TABLE IF NOT EXISTS `fish_records` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL DEFAULT '0',
  `species_key` varchar(64) NOT NULL DEFAULT '',
  `grams` bigint NOT NULL DEFAULT '0',
  `length_cm` double DEFAULT NULL,
  `at` bigint NOT NULL DEFAULT '0',
  `payload` json DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_fr_user` (`user_id`,`at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- keepnets
CREATE TABLE IF NOT EXISTS `keepnets` (
  `username` varchar(32) NOT NULL,
  `rows` int NOT NULL DEFAULT '0',
  `payload` json NOT NULL,
  `updated_at` bigint NOT NULL DEFAULT '0',
  PRIMARY KEY (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- login_tokens
CREATE TABLE IF NOT EXISTS `login_tokens` (
  `token` varchar(64) NOT NULL,
  `logintoken` varchar(96) DEFAULT NULL,
  `username` varchar(32) NOT NULL DEFAULT '',
  `user_id` bigint unsigned NOT NULL DEFAULT '0',
  `expires_at` bigint NOT NULL DEFAULT '0',
  `created_at` bigint NOT NULL DEFAULT '0',
  `used_at` bigint DEFAULT NULL,
  `ip` varchar(64) NOT NULL DEFAULT '',
  `machine_id` varchar(191) NOT NULL DEFAULT '',
  `version` varchar(32) NOT NULL DEFAULT '',
  `sha1_hex` char(40) DEFAULT NULL,
  `ticket` varchar(64) DEFAULT NULL,
  `orig_token` varchar(64) DEFAULT NULL,
  `extra` json DEFAULT NULL,
  PRIMARY KEY (`token`),
  KEY `idx_lt_logintoken` (`logintoken`),
  KEY `idx_lt_user` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- objects
CREATE TABLE IF NOT EXISTS `objects` (
  `guid` char(32) NOT NULL,
  `username` varchar(32) NOT NULL,
  `item_key` varchar(64) NOT NULL DEFAULT '',
  `item_id` int unsigned DEFAULT NULL,
  `parent_guid` char(32) DEFAULT NULL,
  `price` bigint NOT NULL DEFAULT '0',
  `how` varchar(64) NOT NULL DEFAULT '',
  `source` varchar(64) NOT NULL DEFAULT '',
  `created_at` bigint NOT NULL DEFAULT '0',
  `elem` mediumtext NOT NULL,
  PRIMARY KEY (`guid`),
  KEY `idx_objects_user` (`username`),
  KEY `idx_objects_item_id` (`item_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- purchases
CREATE TABLE IF NOT EXISTS `purchases` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `username` varchar(32) NOT NULL DEFAULT '',
  `user_id` bigint unsigned NOT NULL DEFAULT '0',
  `store` varchar(64) NOT NULL DEFAULT '',
  `item_key` varchar(64) NOT NULL DEFAULT '',
  `item_id` int unsigned DEFAULT NULL,
  `price` bigint NOT NULL DEFAULT '0',
  `grams` bigint NOT NULL DEFAULT '0',
  `guid` char(32) DEFAULT NULL,
  `at` bigint NOT NULL DEFAULT '0',
  `payload` json DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_purchases_user` (`username`,`at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- rig
CREATE TABLE IF NOT EXISTS `rig` (
  `username` varchar(64) NOT NULL,
  `rod_guid` char(32) NOT NULL,
  `line_guid` char(32) DEFAULT NULL,
  `reel_guid` char(32) DEFAULT NULL,
  `rig_name` varchar(64) DEFAULT NULL,
  `rig_type` smallint NOT NULL DEFAULT '0',
  `updated_at` bigint NOT NULL,
  PRIMARY KEY (`username`,`rod_guid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- rig_component
CREATE TABLE IF NOT EXISTS `rig_component` (
  `rod_guid` char(32) NOT NULL,
  `slot` smallint NOT NULL,
  `ordinal` smallint NOT NULL DEFAULT '0',
  `guid` char(32) NOT NULL,
  `item_key` varchar(64) NOT NULL DEFAULT '',
  `extra` int NOT NULL DEFAULT '0',
  `updated_at` bigint NOT NULL,
  PRIMARY KEY (`rod_guid`,`slot`,`ordinal`),
  KEY `idx_rig_component_guid` (`guid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- shortcuts
CREATE TABLE IF NOT EXISTS `shortcuts` (
  `username` varchar(64) NOT NULL,
  `slot` smallint NOT NULL,
  `guid` char(32) NOT NULL,
  `item_key` varchar(64) NOT NULL DEFAULT '',
  `updated_at` bigint NOT NULL DEFAULT '0',
  PRIMARY KEY (`username`,`slot`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- skill
CREATE TABLE IF NOT EXISTS `skill` (
  `username` varchar(64) NOT NULL,
  `skill_id` smallint NOT NULL,
  `points` double NOT NULL DEFAULT '0',
  `updated_at` bigint NOT NULL,
  PRIMARY KEY (`username`,`skill_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- skill_def
CREATE TABLE IF NOT EXISTS `skill_def` (
  `cat_id` smallint NOT NULL,
  `node_id` smallint NOT NULL,
  `unlock_pct` double NOT NULL DEFAULT '0',
  `max_level` smallint NOT NULL DEFAULT '0',
  `effect_pct` double NOT NULL DEFAULT '0',
  `group_id` smallint NOT NULL DEFAULT '0',
  `enabled` tinyint NOT NULL DEFAULT '0',
  `ordinal` smallint NOT NULL DEFAULT '0',
  `updated_at` bigint NOT NULL,
  PRIMARY KEY (`cat_id`,`node_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- skill_node
CREATE TABLE IF NOT EXISTS `skill_node` (
  `username` varchar(64) NOT NULL,
  `node_id` smallint NOT NULL,
  `level` smallint NOT NULL DEFAULT '0',
  `updated_at` bigint NOT NULL,
  PRIMARY KEY (`username`,`node_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- skill_points
CREATE TABLE IF NOT EXISTS `skill_points` (
  `username` varchar(64) NOT NULL,
  `granted` int NOT NULL DEFAULT '0',
  `updated_at` bigint NOT NULL,
  PRIMARY KEY (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_companion_bait_fish
CREATE TABLE IF NOT EXISTS `static_companion_bait_fish` (
  `bait_id` varchar(191) NOT NULL,
  `fish_key` varchar(191) NOT NULL,
  `tier` bigint DEFAULT NULL,
  `evidence` bigint DEFAULT NULL,
  PRIMARY KEY (`bait_id`,`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_companion_fish_meta
CREATE TABLE IF NOT EXISTS `static_companion_fish_meta` (
  `fish_key` varchar(191) NOT NULL,
  `total_evidence` bigint DEFAULT NULL,
  `n_baits` bigint DEFAULT NULL,
  PRIMARY KEY (`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_fish_bait_pref
CREATE TABLE IF NOT EXISTS `static_fish_bait_pref` (
  `fish_key` varchar(191) NOT NULL,
  `bait_zh` varchar(191) NOT NULL,
  PRIMARY KEY (`fish_key`,`bait_zh`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_item_catalog
CREATE TABLE IF NOT EXISTS `static_item_catalog` (
  `item_id` varchar(191) NOT NULL,
  `name` text,
  `name_zh` text,
  `kind` text,
  `numeric_id` bigint DEFAULT NULL,
  PRIMARY KEY (`item_id`),
  KEY `idx_catalog_numeric` (`numeric_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_item_def
CREATE TABLE IF NOT EXISTS `static_item_def` (
  `numeric_id` bigint NOT NULL,
  `item_id` text,
  `category` bigint DEFAULT NULL,
  `kind` text,
  `brand` text,
  `attrs_json` text,
  PRIMARY KEY (`numeric_id`),
  KEY `idx_def_item` (`item_id`(191))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_item_price
CREATE TABLE IF NOT EXISTS `static_item_price` (
  `item_id` varchar(191) NOT NULL,
  `silver` bigint DEFAULT NULL,
  `gold` bigint DEFAULT NULL,
  `stock` bigint DEFAULT NULL,
  PRIMARY KEY (`item_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_loc_alias
CREATE TABLE IF NOT EXISTS `static_loc_alias` (
  `alias` varchar(191) NOT NULL,
  `loc` text,
  `note` text,
  PRIMARY KEY (`alias`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_loc_bait
CREATE TABLE IF NOT EXISTS `static_loc_bait` (
  `loc` varchar(191) NOT NULL,
  `bait` varchar(191) NOT NULL,
  `catches` bigint DEFAULT NULL,
  PRIMARY KEY (`loc`,`bait`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_loc_bait_fish
CREATE TABLE IF NOT EXISTS `static_loc_bait_fish` (
  `loc` varchar(191) NOT NULL,
  `bait` varchar(191) NOT NULL,
  `fish_key` varchar(191) NOT NULL,
  `catches` bigint DEFAULT NULL,
  `trophy` bigint DEFAULT NULL,
  `super` bigint DEFAULT NULL,
  PRIMARY KEY (`loc`,`bait`,`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_loc_fish
CREATE TABLE IF NOT EXISTS `static_loc_fish` (
  `loc` varchar(191) NOT NULL,
  `fish_key` varchar(191) NOT NULL,
  `name_zh` text,
  `catches` bigint DEFAULT NULL,
  PRIMARY KEY (`loc`,`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_loc_wiki_fish
CREATE TABLE IF NOT EXISTS `static_loc_wiki_fish` (
  `loc` varchar(191) NOT NULL,
  `fish_key` varchar(191) NOT NULL,
  `fish_zh` text,
  `in_map_roster` bigint DEFAULT NULL,
  `in_habitat` bigint DEFAULT NULL,
  PRIMARY KEY (`loc`,`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_map_fish_roster
CREATE TABLE IF NOT EXISTS `static_map_fish_roster` (
  `loc` varchar(191) NOT NULL,
  `fish_key` varchar(191) NOT NULL,
  `source` text,
  PRIMARY KEY (`loc`,`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_recipe
CREATE TABLE IF NOT EXISTS `static_recipe` (
  `recipe_id` varchar(191) NOT NULL,
  `name` text,
  `output_id` text,
  `output_qty` bigint DEFAULT NULL,
  `materials_json` text,
  `duration_sec` double DEFAULT NULL,
  `kind` text,
  PRIMARY KEY (`recipe_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_shop_item
CREATE TABLE IF NOT EXISTS `static_shop_item` (
  `store_id` varchar(191) NOT NULL,
  `item_id` varchar(191) NOT NULL,
  `silver` bigint DEFAULT NULL,
  `gold` bigint DEFAULT NULL,
  `stock` bigint DEFAULT NULL,
  PRIMARY KEY (`store_id`,`item_id`),
  KEY `idx_shop_item_id` (`item_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_skill_cat_def
CREATE TABLE IF NOT EXISTS `static_skill_cat_def` (
  `cat_id` bigint NOT NULL,
  `ord` bigint DEFAULT NULL,
  `title_key` text,
  `category_kind` bigint DEFAULT NULL,
  PRIMARY KEY (`cat_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_skill_def
CREATE TABLE IF NOT EXISTS `static_skill_def` (
  `cat_id` bigint NOT NULL,
  `node_id` bigint NOT NULL,
  `unlock_pct` double DEFAULT NULL,
  `max_level` bigint DEFAULT NULL,
  `effect_pct` double DEFAULT NULL,
  `group_id` bigint DEFAULT NULL,
  `enabled` bigint DEFAULT NULL,
  `ordinal` bigint DEFAULT NULL,
  PRIMARY KEY (`cat_id`,`node_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_sonar_profile
CREATE TABLE IF NOT EXISTS `static_sonar_profile` (
  `system_id` varchar(191) NOT NULL,
  `cells` bigint DEFAULT NULL,
  `depth_min` double DEFAULT NULL,
  `depth_max` double DEFAULT NULL,
  `size_med` double DEFAULT NULL,
  `size_p90` double DEFAULT NULL,
  PRIMARY KEY (`system_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_species_grade
CREATE TABLE IF NOT EXISTS `static_species_grade` (
  `fish_key` varchar(191) NOT NULL,
  `catches` bigint DEFAULT NULL,
  `trophy` bigint DEFAULT NULL,
  `super` bigint DEFAULT NULL,
  PRIMARY KEY (`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_species_weight
CREATE TABLE IF NOT EXISTS `static_species_weight` (
  `fish_key` varchar(191) NOT NULL,
  `min_g` bigint DEFAULT NULL,
  `max_g` bigint DEFAULT NULL,
  `trophy_g` bigint DEFAULT NULL,
  `src` text,
  `length_k` double DEFAULT NULL,
  `length_p` double DEFAULT NULL,
  `xp_c` double DEFAULT NULL,
  `xp_q` double DEFAULT NULL,
  `trophy_line_g` bigint DEFAULT NULL,
  `super_trophy_line_g` bigint DEFAULT NULL,
  `avg_g` bigint DEFAULT NULL,
  `samples` bigint DEFAULT NULL,
  PRIMARY KEY (`fish_key`),
  KEY `idx_weight_kind` (`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_spot_def
CREATE TABLE IF NOT EXISTS `static_spot_def` (
  `system_id` varchar(191) NOT NULL,
  `map_key` text,
  `kind` bigint DEFAULT NULL,
  `fish_roster` text,
  PRIMARY KEY (`system_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- static_spot_fish
CREATE TABLE IF NOT EXISTS `static_spot_fish` (
  `loc` varchar(191) NOT NULL,
  `spot` varchar(191) NOT NULL,
  `fish_key` varchar(191) NOT NULL,
  `catches` bigint DEFAULT NULL,
  PRIMARY KEY (`loc`,`spot`,`fish_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- users
CREATE TABLE IF NOT EXISTS `users` (
  `username` varchar(32) NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `nickname` varchar(64) NOT NULL DEFAULT '',
  `password_sha256` char(64) DEFAULT NULL,
  `password_sha1` char(40) DEFAULT NULL,
  `level` int NOT NULL DEFAULT '1',
  `stars` int NOT NULL DEFAULT '0',
  `email` varchar(128) NOT NULL DEFAULT '',
  `email_bound` tinyint(1) NOT NULL DEFAULT '0',
  `email_masked` varchar(128) NOT NULL DEFAULT '',
  `premium` tinyint(1) NOT NULL DEFAULT '0',
  `premium_until` bigint NOT NULL DEFAULT '0',
  `gold_minor` bigint NOT NULL DEFAULT '0',
  `silver_minor` bigint NOT NULL DEFAULT '0',
  `avatar` varchar(255) NOT NULL DEFAULT '',
  `muted` tinyint(1) NOT NULL DEFAULT '0',
  `in_prison` tinyint(1) NOT NULL DEFAULT '0',
  `prison_sentence_id` int NOT NULL DEFAULT '0',
  `prison_message` text,
  `required_grams` bigint NOT NULL DEFAULT '0',
  `credited_grams` bigint NOT NULL DEFAULT '0',
  `last_login_at` bigint NOT NULL DEFAULT '0',
  `created_at` bigint NOT NULL DEFAULT '0',
  `machine_ids` json DEFAULT NULL,
  `login_ips` json DEFAULT NULL,
  `extra` json DEFAULT NULL,
  PRIMARY KEY (`username`),
  UNIQUE KEY `uq_users_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
