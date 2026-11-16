CREATE TABLE IF NOT EXISTS "Server" (
        "ServerID"          INTEGER PRIMARY KEY AUTOINCREMENT,
        "Name"              TEXT    NOT NULL,
        "Host"              TEXT    NOT NULL,
        "Port"              INTEGER NOT NULL DEFAULT 5555,
        "EncryptedPassword" TEXT    NOT NULL DEFAULT '',
        "AuthType"          TEXT    NOT NULL DEFAULT 'header',
        "IsEnabled"         INTEGER NOT NULL DEFAULT 1,
        "IsDefault"         INTEGER NOT NULL DEFAULT 0,
        "SortOrder"         INTEGER NOT NULL DEFAULT 0,
        "CreatedDate"       TEXT    NOT NULL,
        "UpdatedDate"       TEXT    NOT NULL
    );
CREATE TABLE IF NOT EXISTS "PanelUser" (
        "UserID"        INTEGER PRIMARY KEY AUTOINCREMENT,
        "Username"      TEXT    NOT NULL UNIQUE,
        "PasswordHash"  TEXT    NOT NULL,
        "SessionVersion" INTEGER NOT NULL DEFAULT 1,
        "CreatedDate"   TEXT    NOT NULL,
        "UpdatedDate"   TEXT    NOT NULL,
        "IsDeleted"     INTEGER NOT NULL DEFAULT 0
    );
CREATE TABLE IF NOT EXISTS "RevokedSession" (
        "TokenHash" TEXT PRIMARY KEY,
        "ExpiresAt" INTEGER NOT NULL
    );
CREATE TABLE IF NOT EXISTS "RPCTrust" (
        "Host" TEXT NOT NULL COLLATE NOCASE,
        "Port" INTEGER NOT NULL,
        "Fingerprint" TEXT NOT NULL,
        PRIMARY KEY ("Host", "Port")
    );
CREATE TABLE IF NOT EXISTS "UserTrafficSample" (
        "UserTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"            INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "HubName"             TEXT    NOT NULL,
        "UserName"            TEXT    NOT NULL,
        "SendBytes"           INTEGER NOT NULL DEFAULT 0,
        "RecvBytes"           INTEGER NOT NULL DEFAULT 0,
        "NumLogin"            INTEGER NOT NULL DEFAULT 0,
        "SampledDate"         TEXT    NOT NULL
    );
CREATE TABLE IF NOT EXISTS "HubTrafficSample" (
        "HubTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"           INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "HubName"            TEXT    NOT NULL,
        "SendBytes"          INTEGER NOT NULL DEFAULT 0,
        "RecvBytes"          INTEGER NOT NULL DEFAULT 0,
        "NumSessions"        INTEGER NOT NULL DEFAULT 0,
        "NumUsers"           INTEGER NOT NULL DEFAULT 0,
        "SampledDate"        TEXT    NOT NULL
    );
CREATE TABLE IF NOT EXISTS "VpnSessionSample" (
        "VpnSessionSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"         INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
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
        UNIQUE ("ServerID", "HubName", "SessionKey")
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
        "ServerID"            INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "HubName"             TEXT NOT NULL,
        "UserName"            TEXT NOT NULL,
        "PasswordHash"        TEXT NOT NULL,
        "UpdatedDate"         TEXT NOT NULL,
        UNIQUE ("ServerID", "HubName", "UserName")
    );
CREATE TABLE IF NOT EXISTS "TrafficQuota" (
        "TrafficQuotaID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"       INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
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
        UNIQUE ("ServerID", "SubjectType", "HubName", "UserKey")
    );
CREATE TABLE IF NOT EXISTS "Setting" (
        "SettingKey"   TEXT PRIMARY KEY,
        "SettingValue" TEXT NOT NULL,
        "UpdatedDate"  TEXT NOT NULL
    );
CREATE TABLE IF NOT EXISTS "AuditLog" (
        "AuditLogID"  INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"    INTEGER REFERENCES "Server"("ServerID") ON DELETE SET NULL,
        "UserID"      INTEGER REFERENCES "PanelUser"("UserID"),
        "Username"    TEXT    NOT NULL,
        "Action"      TEXT    NOT NULL,
        "TargetType"  TEXT    NOT NULL DEFAULT '',
        "TargetKey"   TEXT    NOT NULL DEFAULT '',
        "Detail"      TEXT    NOT NULL DEFAULT '',
        "CreatedDate" TEXT    NOT NULL
    );
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Key" ON "UserTrafficSample"("ServerID", "HubName", "UserName", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Date" ON "UserTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Key" ON "HubTrafficSample"("ServerID", "HubName", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Date" ON "HubTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_AuditLog_Date" ON "AuditLog"("CreatedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_Open" ON "VpnSessionSample"("ServerID", "HubName", "EndedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_User" ON "VpnSessionSample"("ServerID", "HubName", "UserName", "StartedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Session" ON "VpnSessionTrafficSample"("VpnSessionSampleID", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Date" ON "VpnSessionTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_TrafficQuota_Hub" ON "TrafficQuota"("ServerID", "HubName", "SubjectType");
