CREATE TABLE packing_list_states (
    id          BIGINT      NOT NULL AUTO_INCREMENT,
    band_id     BIGINT      NOT NULL,
    revision    BIGINT      NOT NULL DEFAULT 0,
    generation  BIGINT      NOT NULL DEFAULT 1,
    created_at  DATETIME(6) NOT NULL,
    updated_at  DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_packing_list_state_band (band_id),
    CONSTRAINT fk_packing_list_state_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_packing_list_state_revision CHECK (revision >= 0),
    CONSTRAINT ck_packing_list_state_generation CHECK (generation > 0)
);

CREATE TABLE packing_bags (
    id                    CHAR(36)     NOT NULL,
    band_id               BIGINT       NOT NULL,
    name                  VARCHAR(200) NOT NULL,
    position              INT          NOT NULL DEFAULT 0,
    status                VARCHAR(20)  NOT NULL DEFAULT 'open',
    created_at            DATETIME(6)  NOT NULL,
    updated_at            DATETIME(6)  NOT NULL,
    created_by_user_id    BIGINT       NULL,
    created_by_username   VARCHAR(150) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    KEY idx_packing_bags_band_position (band_id, position, id),
    CONSTRAINT fk_packing_bags_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT ck_packing_bags_status CHECK (status IN ('open','packed','stays_here'))
);

CREATE TABLE packing_items (
    id                    CHAR(36)     NOT NULL,
    band_id               BIGINT       NOT NULL,
    bag_id                CHAR(36)     NOT NULL,
    name                  VARCHAR(200) NOT NULL,
    position              INT          NOT NULL DEFAULT 0,
    status                VARCHAR(20)  NOT NULL DEFAULT 'open',
    created_at            DATETIME(6)  NOT NULL,
    updated_at            DATETIME(6)  NOT NULL,
    created_by_user_id    BIGINT       NULL,
    created_by_username   VARCHAR(150) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    KEY idx_packing_items_bag_position (band_id, bag_id, position, id),
    CONSTRAINT fk_packing_items_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_packing_items_bag FOREIGN KEY (bag_id) REFERENCES packing_bags (id) ON DELETE CASCADE,
    CONSTRAINT ck_packing_items_status CHECK (status IN ('open','packed','stays_here'))
);

CREATE TABLE packing_photos (
    id                    CHAR(36)     NOT NULL,
    band_id               BIGINT       NOT NULL,
    bag_id                CHAR(36)     NULL,
    item_id               CHAR(36)     NULL,
    file_path             VARCHAR(255) NOT NULL,
    original_filename     VARCHAR(255) NOT NULL,
    size_bytes            BIGINT       NOT NULL DEFAULT 0,
    position              INT          NOT NULL DEFAULT 0,
    created_at            DATETIME(6)  NOT NULL,
    created_by_user_id    BIGINT       NULL,
    created_by_username   VARCHAR(150) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    UNIQUE KEY uq_packing_photos_path (file_path),
    KEY idx_packing_photos_bag (band_id, bag_id, position, id),
    KEY idx_packing_photos_item (band_id, item_id, position, id),
    CONSTRAINT fk_packing_photos_band FOREIGN KEY (band_id) REFERENCES bands (id),
    CONSTRAINT fk_packing_photos_bag FOREIGN KEY (bag_id) REFERENCES packing_bags (id) ON DELETE CASCADE,
    CONSTRAINT fk_packing_photos_item FOREIGN KEY (item_id) REFERENCES packing_items (id) ON DELETE CASCADE,
    CONSTRAINT ck_packing_photos_owner CHECK ((bag_id IS NULL) <> (item_id IS NULL)),
    CONSTRAINT ck_packing_photos_size CHECK (size_bytes >= 0)
);

CREATE TABLE packing_sync_events (
    id                    BIGINT       NOT NULL AUTO_INCREMENT,
    band_id               BIGINT       NOT NULL,
    event_id              VARCHAR(64)  NOT NULL,
    device_id             VARCHAR(64)  NOT NULL,
    actor_user_id         BIGINT       NOT NULL,
    actor_username        VARCHAR(150) NOT NULL DEFAULT '',
    payload_hash          CHAR(64)     NOT NULL,
    response_json         LONGTEXT     NOT NULL,
    client_created_at     DATETIME(6)  NOT NULL,
    created_at            DATETIME(6)  NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_packing_sync_event (band_id, event_id),
    KEY idx_packing_sync_actor (band_id, actor_user_id, created_at),
    CONSTRAINT fk_packing_sync_band FOREIGN KEY (band_id) REFERENCES bands (id)
);
