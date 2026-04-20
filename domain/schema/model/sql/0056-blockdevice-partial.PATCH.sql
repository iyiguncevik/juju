-- This migration adds a provenance column to block_device.
--
-- SQLite does not support ALTER TABLE DROP CONSTRAINT, so we must use the
-- create-copy-drop-rename pattern for block_device AND all child tables
-- that reference it via foreign keys (block_device_link_device and
-- storage_volume_attachment). The foreign_keys pragma is ON and cannot be
-- toggled mid-transaction.
--
-- See https://www.sqlite.org/lang_altertable.html Section 7.

-- ============================================================================
-- Step 1: Create the new lookup table.
-- ============================================================================

CREATE TABLE block_device_provenance (
    id INT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO block_device_provenance VALUES
(0, 'provider'),
(1, 'machine');

-- ============================================================================
-- Step 2: Drop ALL triggers on the three tables we will recreate.
--         SQLite drops triggers automatically when a table is dropped, but we
--         drop them explicitly first to be clear about what we are rebuilding.
-- ============================================================================

-- noqa: disable=all

-- Triggers on block_device:
DROP TRIGGER trg_log_block_device_insert;
DROP TRIGGER trg_log_block_device_update;
DROP TRIGGER trg_log_block_device_delete;
DROP TRIGGER trg_log_custom_storage_attachment_block_device_update;

-- Triggers on block_device_link_device:
DROP TRIGGER trg_log_custom_storage_attachment_block_device_link_device_insert;
DROP TRIGGER trg_log_custom_storage_attachment_block_device_link_device_update;
DROP TRIGGER trg_log_custom_storage_attachment_block_device_link_device_delete;

-- Triggers on storage_volume_attachment:
DROP TRIGGER trg_storage_volume_attachment_guard_life;
DROP TRIGGER trg_log_custom_machine_uuid_lifecycle_with_dependants_storage_volume_attachment_delete;
DROP TRIGGER trg_log_storage_volume_attachment_insert_life_machine_provisioning;
DROP TRIGGER trg_log_storage_volume_attachment_update_life_machine_provisioning;
DROP TRIGGER trg_log_storage_volume_attachment_delete_life_machine_provisioning;
DROP TRIGGER trg_log_storage_volume_attachment_insert_life_model_provisioning;
DROP TRIGGER trg_log_storage_volume_attachment_update_life_model_provisioning;
DROP TRIGGER trg_log_storage_volume_attachment_delete_life_model_provisioning;
DROP TRIGGER trg_log_custom_storage_attachment_storage_volume_attachment_insert;
DROP TRIGGER trg_log_custom_storage_attachment_storage_volume_attachment_update;
DROP TRIGGER trg_log_custom_storage_attachment_storage_volume_attachment_delete;
DROP TRIGGER trg_log_storage_volume_insert_life_machine_provisioning_on_attachment;
DROP TRIGGER trg_log_storage_volume_delete_life_machine_provisioning_last_attachment;

-- Trigger on storage_volume that references storage_volume_attachment in body:
DROP TRIGGER trg_log_storage_volume_update_life_machine_provisioning;

-- noqa: enable=all

-- ============================================================================
-- Step 3: Create block_device_new with the added provenance column.
-- ============================================================================

CREATE TABLE block_device_new (
    uuid TEXT NOT NULL PRIMARY KEY,
    machine_uuid TEXT NOT NULL,
    name TEXT,
    hardware_id TEXT,
    wwn TEXT,
    serial_id TEXT,
    bus_address TEXT,
    size_mib INT,
    mount_point TEXT,
    in_use BOOLEAN,
    filesystem_label TEXT,
    host_filesystem_uuid TEXT,
    filesystem_type TEXT,
    provenance INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_block_device_machine
    FOREIGN KEY (machine_uuid)
    REFERENCES machine (uuid),
    CONSTRAINT fk_block_device_provenance
    FOREIGN KEY (provenance)
    REFERENCES block_device_provenance (id)
);

INSERT INTO block_device_new
SELECT
    uuid,
    machine_uuid,
    name,
    hardware_id,
    wwn,
    serial_id,
    bus_address,
    size_mib,
    mount_point,
    in_use,
    filesystem_label,
    host_filesystem_uuid,
    filesystem_type,
    0 AS provenance
FROM block_device;

-- ============================================================================
-- Step 4: Recreate block_device_link_device with FK pointing to
--         block_device_new (will be auto-updated on rename).
-- ============================================================================

CREATE TABLE block_device_link_device_new (
    block_device_uuid TEXT NOT NULL,
    machine_uuid TEXT NOT NULL,
    name TEXT NOT NULL,
    CONSTRAINT fk_block_device_link_device
    FOREIGN KEY (block_device_uuid)
    REFERENCES block_device_new (uuid),
    PRIMARY KEY (block_device_uuid, name),
    CONSTRAINT fk_block_device_link_machine
    FOREIGN KEY (machine_uuid)
    REFERENCES machine (uuid)
);

INSERT INTO block_device_link_device_new
SELECT
    block_device_uuid,
    machine_uuid,
    name
FROM block_device_link_device;

DROP INDEX idx_block_device_link_device;
DROP INDEX idx_block_device_link_device_name_machine;
DROP INDEX idx_block_device_link_device_device;
DROP TABLE block_device_link_device;

-- ============================================================================
-- Step 5: Recreate storage_volume_attachment with FK pointing to
--         block_device_new (will be auto-updated on rename).
-- ============================================================================

CREATE TABLE storage_volume_attachment_new (
    uuid TEXT NOT NULL PRIMARY KEY,
    storage_volume_uuid TEXT NOT NULL,
    net_node_uuid TEXT NOT NULL,
    life_id INT NOT NULL,
    provision_scope_id INT NOT NULL,
    provider_id TEXT,
    block_device_uuid TEXT,
    read_only BOOLEAN,
    CONSTRAINT fk_storage_volume_attachment_vol
    FOREIGN KEY (storage_volume_uuid)
    REFERENCES storage_volume (uuid),
    CONSTRAINT fk_storage_volume_attachment_node
    FOREIGN KEY (net_node_uuid)
    REFERENCES net_node (uuid),
    CONSTRAINT fk_storage_volume_attachment_life
    FOREIGN KEY (life_id)
    REFERENCES life (id),
    CONSTRAINT fk_storage_volume_attachment_block
    FOREIGN KEY (block_device_uuid)
    REFERENCES block_device_new (uuid),
    CONSTRAINT fk_storage_volume_attachment_provision_scope_id
    FOREIGN KEY (provision_scope_id)
    REFERENCES storage_provision_scope (id)
);

INSERT INTO storage_volume_attachment_new
SELECT
    uuid,
    storage_volume_uuid,
    net_node_uuid,
    life_id,
    provision_scope_id,
    provider_id,
    block_device_uuid,
    read_only
FROM storage_volume_attachment;

DROP INDEX idx_storage_volume_attachment_volume_uuid;
DROP INDEX idx_storage_volume_attachment_net_node_uuid;
DROP INDEX idx_storage_volume_attachment_block_device_uuid;
DROP TABLE storage_volume_attachment;

-- ============================================================================
-- Step 6: Drop old block_device (no more FK dependents) and rename new tables.
--         SQLite >= 3.26.0 auto-updates FK references in child tables when
--         a parent table is renamed.
-- ============================================================================

DROP INDEX idx_block_device_name;
DROP TABLE block_device;

ALTER TABLE block_device_new RENAME TO block_device;
ALTER TABLE block_device_link_device_new RENAME TO block_device_link_device;
ALTER TABLE storage_volume_attachment_new RENAME TO storage_volume_attachment;

-- ============================================================================
-- Step 7: Recreate all indexes.
-- ============================================================================

-- block_device indexes:
CREATE UNIQUE INDEX idx_block_device_name
ON block_device (machine_uuid, name);

-- block_device_link_device indexes:
CREATE UNIQUE INDEX idx_block_device_link_device
ON block_device_link_device (block_device_uuid, name);

CREATE UNIQUE INDEX idx_block_device_link_device_name_machine
ON block_device_link_device (name, machine_uuid);

CREATE INDEX idx_block_device_link_device_device
ON block_device_link_device (block_device_uuid);

-- storage_volume_attachment indexes:
CREATE UNIQUE INDEX idx_storage_volume_attachment_volume_uuid
ON storage_volume_attachment (storage_volume_uuid);

CREATE INDEX idx_storage_volume_attachment_net_node_uuid
ON storage_volume_attachment (net_node_uuid);

CREATE INDEX idx_storage_volume_attachment_block_device_uuid
ON storage_volume_attachment (block_device_uuid);

-- ============================================================================
-- Step 8: Recreate all triggers.
-- ============================================================================

-- noqa: disable=all

-- --------------------------------------------------------------------------
-- block_device triggers
-- --------------------------------------------------------------------------

CREATE TRIGGER trg_log_block_device_insert
AFTER INSERT ON block_device FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (1, 10002, NEW.machine_uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_block_device_update
AFTER UPDATE ON block_device FOR EACH ROW
WHEN 
	NEW.uuid != OLD.uuid OR
	NEW.machine_uuid != OLD.machine_uuid OR
	(NEW.name != OLD.name OR (NEW.name IS NOT NULL AND OLD.name IS NULL) OR (NEW.name IS NULL AND OLD.name IS NOT NULL)) OR
	(NEW.hardware_id != OLD.hardware_id OR (NEW.hardware_id IS NOT NULL AND OLD.hardware_id IS NULL) OR (NEW.hardware_id IS NULL AND OLD.hardware_id IS NOT NULL)) OR
	(NEW.wwn != OLD.wwn OR (NEW.wwn IS NOT NULL AND OLD.wwn IS NULL) OR (NEW.wwn IS NULL AND OLD.wwn IS NOT NULL)) OR
	(NEW.serial_id != OLD.serial_id OR (NEW.serial_id IS NOT NULL AND OLD.serial_id IS NULL) OR (NEW.serial_id IS NULL AND OLD.serial_id IS NOT NULL)) OR
	(NEW.bus_address != OLD.bus_address OR (NEW.bus_address IS NOT NULL AND OLD.bus_address IS NULL) OR (NEW.bus_address IS NULL AND OLD.bus_address IS NOT NULL)) OR
	(NEW.size_mib != OLD.size_mib OR (NEW.size_mib IS NOT NULL AND OLD.size_mib IS NULL) OR (NEW.size_mib IS NULL AND OLD.size_mib IS NOT NULL)) OR
	(NEW.mount_point != OLD.mount_point OR (NEW.mount_point IS NOT NULL AND OLD.mount_point IS NULL) OR (NEW.mount_point IS NULL AND OLD.mount_point IS NOT NULL)) OR
	(NEW.in_use != OLD.in_use OR (NEW.in_use IS NOT NULL AND OLD.in_use IS NULL) OR (NEW.in_use IS NULL AND OLD.in_use IS NOT NULL)) OR
	(NEW.filesystem_label != OLD.filesystem_label OR (NEW.filesystem_label IS NOT NULL AND OLD.filesystem_label IS NULL) OR (NEW.filesystem_label IS NULL AND OLD.filesystem_label IS NOT NULL)) OR
	(NEW.host_filesystem_uuid != OLD.host_filesystem_uuid OR (NEW.host_filesystem_uuid IS NOT NULL AND OLD.host_filesystem_uuid IS NULL) OR (NEW.host_filesystem_uuid IS NULL AND OLD.host_filesystem_uuid IS NOT NULL)) OR
	(NEW.filesystem_type != OLD.filesystem_type OR (NEW.filesystem_type IS NOT NULL AND OLD.filesystem_type IS NULL) OR (NEW.filesystem_type IS NULL AND OLD.filesystem_type IS NOT NULL)) OR
	(NEW.provenance != OLD.provenance OR (NEW.provenance IS NOT NULL AND OLD.provenance IS NULL) OR (NEW.provenance IS NULL AND OLD.provenance IS NOT NULL)) 
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (2, 10002, OLD.machine_uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_block_device_delete
AFTER DELETE ON block_device FOR EACH ROW
BEGIN
	INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
	VALUES (4, 10002, OLD.machine_uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_custom_storage_attachment_block_device_update
AFTER UPDATE ON block_device FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 2, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_volume_attachment sva
    JOIN storage_instance_volume siv ON siv.storage_volume_uuid = sva.storage_volume_uuid
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE sva.block_device_uuid = NEW.uuid;
END;

-- --------------------------------------------------------------------------
-- block_device_link_device triggers
-- --------------------------------------------------------------------------

CREATE TRIGGER trg_log_custom_storage_attachment_block_device_link_device_insert
AFTER INSERT ON block_device_link_device FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 1, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_volume_attachment sva
    JOIN storage_instance_volume siv ON siv.storage_volume_uuid = sva.storage_volume_uuid
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE sva.block_device_uuid = NEW.block_device_uuid;
END;

CREATE TRIGGER trg_log_custom_storage_attachment_block_device_link_device_update
AFTER UPDATE ON block_device_link_device FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 2, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_volume_attachment sva
    JOIN storage_instance_volume siv ON siv.storage_volume_uuid = sva.storage_volume_uuid
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE sva.block_device_uuid = NEW.block_device_uuid;
END;

CREATE TRIGGER trg_log_custom_storage_attachment_block_device_link_device_delete
AFTER DELETE ON block_device_link_device FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 4, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_volume_attachment sva
    JOIN storage_instance_volume siv ON siv.storage_volume_uuid = sva.storage_volume_uuid
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE sva.block_device_uuid = OLD.block_device_uuid;
END;

-- --------------------------------------------------------------------------
-- storage_volume_attachment triggers
-- --------------------------------------------------------------------------

CREATE TRIGGER trg_storage_volume_attachment_guard_life
    BEFORE UPDATE ON storage_volume_attachment
    FOR EACH ROW
    WHEN NEW.life_id < OLD.life_id
    BEGIN
        SELECT RAISE(FAIL, 'Cannot transition life for storage_volume_attachment backwards');
    END;

CREATE TRIGGER trg_log_custom_machine_uuid_lifecycle_with_dependants_storage_volume_attachment_delete
AFTER DELETE ON storage_volume_attachment FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 4, 3, m.uuid, DATETIME('now')
    FROM machine AS m
    WHERE m.net_node_uuid = OLD.net_node_uuid;
END;

CREATE TRIGGER trg_log_storage_volume_attachment_insert_life_machine_provisioning
AFTER INSERT ON storage_volume_attachment
FOR EACH ROW
	WHEN NEW.provision_scope_id = 1
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (1, 12, NEW.net_node_uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_storage_volume_attachment_update_life_machine_provisioning
AFTER UPDATE ON storage_volume_attachment
FOR EACH ROW
	WHEN NEW.provision_scope_id = 1
	AND NEW.life_id != OLD.life_id
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (2, 12, NEW.net_node_uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_storage_volume_attachment_delete_life_machine_provisioning
AFTER DELETE ON storage_volume_attachment
FOR EACH ROW
	WHEN OLD.provision_scope_id = 1
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (4, 12, OLD.net_node_uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_storage_volume_attachment_insert_life_model_provisioning
AFTER INSERT ON storage_volume_attachment
FOR EACH ROW
	WHEN NEW.provision_scope_id = 0
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (1, 13, NEW.uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_storage_volume_attachment_update_life_model_provisioning
AFTER UPDATE ON storage_volume_attachment
FOR EACH ROW
	WHEN NEW.provision_scope_id = 0
	AND NEW.life_id != OLD.life_id
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (2, 13, NEW.uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_storage_volume_attachment_delete_life_model_provisioning
AFTER DELETE ON storage_volume_attachment
FOR EACH ROW
	WHEN OLD.provision_scope_id = 0
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    VALUES (4, 13, OLD.uuid, DATETIME('now', 'utc'));
END;

CREATE TRIGGER trg_log_custom_storage_attachment_storage_volume_attachment_insert
AFTER INSERT ON storage_volume_attachment FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 1, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_instance_volume siv
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE siv.storage_volume_uuid = NEW.storage_volume_uuid;
END;

CREATE TRIGGER trg_log_custom_storage_attachment_storage_volume_attachment_update
AFTER UPDATE ON storage_volume_attachment FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 2, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_instance_volume siv
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE siv.storage_volume_uuid = NEW.storage_volume_uuid;
END;

CREATE TRIGGER trg_log_custom_storage_attachment_storage_volume_attachment_delete
AFTER DELETE ON storage_volume_attachment FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 4, 20, sa.uuid, DATETIME('now', 'utc')
    FROM storage_instance_volume siv
    JOIN storage_attachment sa ON sa.storage_instance_uuid = siv.storage_instance_uuid
    WHERE siv.storage_volume_uuid = OLD.storage_volume_uuid;
END;

CREATE TRIGGER trg_log_storage_volume_insert_life_machine_provisioning_on_attachment
AFTER INSERT ON storage_volume_attachment FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT 1,
           10,
           NEW.net_node_uuid,
           DATETIME('now', 'utc')
    FROM   storage_volume s
    WHERE  1 == (SELECT COUNT(*)
                 FROM   storage_volume_attachment
                 WHERE  storage_volume_uuid = NEW.storage_volume_uuid)
    AND    s.uuid = NEW.storage_volume_uuid
    AND    s.provision_scope_id = 1;
END;

CREATE TRIGGER trg_log_storage_volume_update_life_machine_provisioning
AFTER UPDATE ON storage_volume
FOR EACH ROW
	WHEN NEW.provision_scope_id = 1
	AND  NEW.life_id != OLD.life_id
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT DISTINCT 2,
           			10,
           			a.net_node_uuid,
           			DATETIME('now', 'utc')
    FROM  storage_volume_attachment a
    WHERE storage_volume_uuid = NEW.uuid;
END;

CREATE TRIGGER trg_log_storage_volume_delete_life_machine_provisioning_last_attachment
AFTER DELETE ON storage_volume_attachment FOR EACH ROW
BEGIN
    INSERT INTO change_log (edit_type_id, namespace_id, changed, created_at)
    SELECT DISTINCT 4,
           			10,
           			OLD.net_node_uuid,
           			DATETIME('now', 'utc')
    FROM   storage_volume s
    WHERE  0 == (SELECT COUNT(*)
                 FROM   storage_volume_attachment
                 WHERE  storage_volume_uuid = OLD.storage_volume_uuid)
    AND    s.uuid = OLD.storage_volume_uuid
    AND    s.provision_scope_id = 1;
END;

-- noqa: enable=all
