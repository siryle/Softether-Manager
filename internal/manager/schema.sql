CREATE TABLE IF NOT EXISTS "PanelUser" (
        "UserID"        INTEGER PRIMARY KEY AUTOINCREMENT,
        "Username"      TEXT    NOT NULL UNIQUE,
        "PasswordHash"  TEXT    NOT NULL,
        "CreatedDate"   TEXT    NOT NULL,
        "UpdatedDate"   TEXT    NOT NULL,
        "IsDeleted"     INTEGER NOT NULL DEFAULT 0
    );
CREATE TABLE IF NOT EXISTS "UserTrafficSample" (
        "UserTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "HubName"             TEXT    NOT NULL,
        "UserName"            TEXT    NOT NULL,
        "SendBytes"           INTEGER NOT NULL DEFAULT 0,
        "RecvBytes"           INTEGER NOT NULL DEFAULT 0,
        "NumLogin"            INTEGER NOT NULL DEFAULT 0,
        "SampledDate"         TEXT    NOT NULL
    );
CREATE TABLE IF NOT EXISTS "HubTrafficSample" (
        "HubTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "HubName"            TEXT    NOT NULL,
        "SendBytes"          INTEGER NOT NULL DEFAULT 0,
        "RecvBytes"          INTEGER NOT NULL DEFAULT 0,
        "NumSessions"        INTEGER NOT NULL DEFAULT 0,
        "NumUsers"           INTEGER NOT NULL DEFAULT 0,
        "SampledDate"        TEXT    NOT NULL
    );
CREATE TABLE IF NOT EXISTS "VpnSessionSample" (
        "VpnSessionSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "HubName"          TEXT NOT NULL,
        "UserName"         TEXT NOT NULL,
        "SessionKey"       TEXT NOT NULL,
        "ClientIP"         TEXT NOT NULL DEFAULT '',
        "ClientHostname"   TEXT NOT NULL DEFAULT '',
        "StartedDate"      TEXT NOT NULL,
        "EndedDate"        TEXT,
        "LastSeenDate"     TEXT NOT NULL,
        "BytesTotal"       INTEGER NOT NULL DEFAULT 0,
        "PacketsTotal"     INTEGER NOT NULL DEFAULT 0,
        "DownloadBytes"    INTEGER NOT NULL DEFAULT 0,
        "UploadBytes"      INTEGER NOT NULL DEFAULT 0,
        UNIQUE ("HubName", "SessionKey")
    );
CREATE TABLE IF NOT EXISTS "VpnSessionTrafficSample" (
        "VpnSessionTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "VpnSessionSampleID" INTEGER NOT NULL,
        "DownloadBytes"      INTEGER NOT NULL DEFAULT 0,
        "UploadBytes"        INTEGER NOT NULL DEFAULT 0,
        "TotalBytes"         INTEGER NOT NULL DEFAULT 0,
        "SampledDate"        TEXT    NOT NULL
    );
CREATE TABLE IF NOT EXISTS "VpnUserCredential" (
        "VpnUserCredentialID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "HubName"             TEXT NOT NULL,
        "UserName"            TEXT NOT NULL,
        "PasswordHash"        TEXT NOT NULL,
        "UpdatedDate"         TEXT NOT NULL,
        UNIQUE ("HubName", "UserName")
    );
CREATE TABLE IF NOT EXISTS "TrafficQuota" (
        "TrafficQuotaID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "SubjectType"    TEXT    NOT NULL,
        "HubName"        TEXT    NOT NULL,
        "UserName"       TEXT    NOT NULL DEFAULT '',
        "UserKey"        TEXT    NOT NULL DEFAULT '',
        "LimitBytes"     INTEGER NOT NULL DEFAULT 0,
        "Metric"         TEXT    NOT NULL DEFAULT 'total',
        "IsEnabled"      INTEGER NOT NULL DEFAULT 1,
        "BaseSendBytes"  INTEGER NOT NULL DEFAULT 0,
        "BaseRecvBytes"  INTEGER NOT NULL DEFAULT 0,
        "LastSendBytes"  INTEGER NOT NULL DEFAULT 0,
        "LastRecvBytes"  INTEGER NOT NULL DEFAULT 0,
        "CycleStartDate" TEXT    NOT NULL,
        "ExceededDate"   TEXT,
        "EnforcedDate"   TEXT,
        "RestoreState"   TEXT    NOT NULL DEFAULT '',
        "CreatedDate"    TEXT    NOT NULL,
        "UpdatedDate"    TEXT    NOT NULL,
        UNIQUE ("SubjectType", "HubName", "UserKey")
    );
CREATE TABLE IF NOT EXISTS "Setting" (
        "SettingKey"   TEXT PRIMARY KEY,
        "SettingValue" TEXT NOT NULL,
        "UpdatedDate"  TEXT NOT NULL
    );
CREATE TABLE IF NOT EXISTS "AuditLog" (
        "AuditLogID"  INTEGER PRIMARY KEY AUTOINCREMENT,
        "UserID"      INTEGER REFERENCES "PanelUser"("UserID"),
        "Username"    TEXT    NOT NULL,
        "Action"      TEXT    NOT NULL,
        "TargetType"  TEXT    NOT NULL DEFAULT '',
        "TargetKey"   TEXT    NOT NULL DEFAULT '',
        "Detail"      TEXT    NOT NULL DEFAULT '',
        "CreatedDate" TEXT    NOT NULL
    );
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Key" ON "UserTrafficSample"("HubName", "UserName", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Date" ON "UserTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Key" ON "HubTrafficSample"("HubName", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Date" ON "HubTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_AuditLog_Date" ON "AuditLog"("CreatedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_Open" ON "VpnSessionSample"("HubName", "EndedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_User" ON "VpnSessionSample"("HubName", "UserName", "StartedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Session" ON "VpnSessionTrafficSample"("VpnSessionSampleID", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Date" ON "VpnSessionTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_TrafficQuota_Hub" ON "TrafficQuota"("HubName", "SubjectType");
