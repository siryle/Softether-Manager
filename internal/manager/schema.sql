CREATE TABLE IF NOT EXISTS "Server" (
        "ServerID"          INTEGER PRIMARY KEY AUTOINCREMENT,
        "Name"              TEXT    NOT NULL,
        "Host"              TEXT    NOT NULL,
        "Port"              INTEGER NOT NULL DEFAULT 5555,
        "EncryptedPassword" TEXT    NOT NULL DEFAULT '',
        "AuthType"          TEXT    NOT NULL DEFAULT 'header',
        "SkipCertCheck"     INTEGER NOT NULL DEFAULT 0,
        "IsEnabled"         INTEGER NOT NULL DEFAULT 1,
        "IsDefault"         INTEGER NOT NULL DEFAULT 0,
        "SortOrder"         INTEGER NOT NULL DEFAULT 0,
        "PublicHost"        TEXT    NOT NULL DEFAULT '',
        "PublicPort"        INTEGER NOT NULL DEFAULT 0,
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
        "PendingFingerprint" TEXT NOT NULL DEFAULT '',
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
        "SampledDate"         TEXT    NOT NULL,
        "SampledTs"           INTEGER NOT NULL DEFAULT 0,
        "DeltaSendBytes"      INTEGER NOT NULL DEFAULT 0,
        "DeltaRecvBytes"      INTEGER NOT NULL DEFAULT 0,
        "HasDelta"            INTEGER NOT NULL DEFAULT 0
    );
CREATE TABLE IF NOT EXISTS "HubTrafficSample" (
        "HubTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"           INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "HubName"            TEXT    NOT NULL,
        "SendBytes"          INTEGER NOT NULL DEFAULT 0,
        "RecvBytes"          INTEGER NOT NULL DEFAULT 0,
        "NumSessions"        INTEGER NOT NULL DEFAULT 0,
        "NumUsers"           INTEGER NOT NULL DEFAULT 0,
        "SampledDate"        TEXT    NOT NULL,
        "SampledTs"          INTEGER NOT NULL DEFAULT 0,
        "DeltaSendBytes"     INTEGER NOT NULL DEFAULT 0,
        "DeltaRecvBytes"     INTEGER NOT NULL DEFAULT 0,
        "HasDelta"           INTEGER NOT NULL DEFAULT 0
    );
CREATE TABLE IF NOT EXISTS "VpnSessionSample" (
        "VpnSessionSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"         INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "HubName"          TEXT NOT NULL,
        "UserName"         TEXT NOT NULL,
        "SessionKey"       TEXT NOT NULL,
        "ClientIP"         TEXT NOT NULL DEFAULT '',
        "VirtualIP"        TEXT NOT NULL DEFAULT '',
        "ClientHostname"   TEXT NOT NULL DEFAULT '',
        "StartedDate"      TEXT NOT NULL,
        "EndedDate"        TEXT,
        "LastSeenDate"     TEXT NOT NULL,
        "BytesTotal"       INTEGER NOT NULL DEFAULT 0,
        "PacketsTotal"     INTEGER NOT NULL DEFAULT 0,
        "DownloadBytes"    INTEGER NOT NULL DEFAULT 0,
        "UploadBytes"      INTEGER NOT NULL DEFAULT 0,
        "StartedTs"        INTEGER NOT NULL DEFAULT 0,
        "EndedTs"          INTEGER,
        "RealName"         TEXT NOT NULL DEFAULT '',
        "Note"             TEXT NOT NULL DEFAULT '',
        "IpLocation"       TEXT NOT NULL DEFAULT '',
        "IpISP"            TEXT NOT NULL DEFAULT '',
        "IpFull"           TEXT NOT NULL DEFAULT '',
        "Protocol"         TEXT NOT NULL DEFAULT '',
        "DisconnectReason" TEXT NOT NULL DEFAULT '',
        "ServerPort"       INTEGER NOT NULL DEFAULT 0,
        "ClientPort"       INTEGER NOT NULL DEFAULT 0,
        UNIQUE ("ServerID", "HubName", "SessionKey")
    );
CREATE TABLE IF NOT EXISTS "VpnSessionTrafficSample" (
        "VpnSessionTrafficSampleID" INTEGER PRIMARY KEY AUTOINCREMENT,
        "VpnSessionSampleID" INTEGER NOT NULL,
        "DownloadBytes"      INTEGER NOT NULL DEFAULT 0,
        "UploadBytes"        INTEGER NOT NULL DEFAULT 0,
        "TotalBytes"         INTEGER NOT NULL DEFAULT 0,
        "SampledDate"        TEXT    NOT NULL,
        "SampledTs"          INTEGER NOT NULL DEFAULT 0
    );
CREATE TABLE IF NOT EXISTS "TrafficCursor" (
        "ServerID"        INTEGER NOT NULL REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "SubjectType"     TEXT    NOT NULL,
        "HubName"         TEXT    NOT NULL,
        "UserKey"         TEXT    NOT NULL DEFAULT '',
        "LastSendBytes"   INTEGER NOT NULL DEFAULT 0,
        "LastRecvBytes"   INTEGER NOT NULL DEFAULT 0,
        "ServerStartTime" TEXT    NOT NULL DEFAULT '',
        "LastSampledTs"   INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY ("ServerID", "SubjectType", "HubName", "UserKey")
    );
CREATE TABLE IF NOT EXISTS "TrafficRollupHourly" (
        "ServerID"    INTEGER NOT NULL REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "SubjectType" TEXT    NOT NULL,
        "HubName"     TEXT    NOT NULL,
        "UserKey"     TEXT    NOT NULL DEFAULT '',
        "UserName"    TEXT    NOT NULL DEFAULT '',
        "BucketTs"    INTEGER NOT NULL,
        "SendBytes"   INTEGER NOT NULL DEFAULT 0,
        "RecvBytes"   INTEGER NOT NULL DEFAULT 0,
        "MaxSessions" INTEGER NOT NULL DEFAULT 0,
        "SampleCount" INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY ("ServerID", "SubjectType", "HubName", "UserKey", "BucketTs")
    );
CREATE TABLE IF NOT EXISTS "LogSyncCursor" (
        "ServerID"    INTEGER NOT NULL REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "HubName"     TEXT    NOT NULL DEFAULT '',
        "FilePath"    TEXT    NOT NULL,
        "ByteOffset"  INTEGER NOT NULL DEFAULT 0,
        "FileSize"    INTEGER NOT NULL DEFAULT 0,
        "UpdatedDate" TEXT    NOT NULL DEFAULT '',
        PRIMARY KEY ("ServerID", "HubName", "FilePath")
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
CREATE TABLE IF NOT EXISTS "WireGuardPeer" (
        "WireGuardPeerID"     INTEGER PRIMARY KEY AUTOINCREMENT,
        "ServerID"            INTEGER NOT NULL DEFAULT 1 REFERENCES "Server"("ServerID") ON DELETE CASCADE,
        "PublicKey"           TEXT    NOT NULL,
        "HubName"             TEXT    NOT NULL,
        "UserName"            TEXT    NOT NULL,
        "Label"               TEXT    NOT NULL DEFAULT '',
        "AssignedIP"          TEXT    NOT NULL DEFAULT '',
        "EncryptedPrivateKey" TEXT    NOT NULL DEFAULT '',
        "CreatedDate"         TEXT    NOT NULL,
        "UpdatedDate"         TEXT    NOT NULL,
        UNIQUE ("ServerID", "PublicKey")
    );
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Key" ON "UserTrafficSample"("ServerID", "HubName", "UserName", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_History" ON "UserTrafficSample"("ServerID", "HubName", "UserName" COLLATE NOCASE, julianday("SampledDate"));
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Retention" ON "UserTrafficSample"(datetime("SampledDate"));
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Retention" ON "HubTrafficSample"(datetime("SampledDate"));
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Date" ON "UserTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Key" ON "HubTrafficSample"("ServerID", "HubName", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_History" ON "HubTrafficSample"("ServerID", "HubName", julianday("SampledDate"));
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_ServerHistory" ON "HubTrafficSample"("ServerID", julianday("SampledDate"));
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Date" ON "HubTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_AuditLog_Date" ON "AuditLog"("CreatedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_Open" ON "VpnSessionSample"("ServerID", "HubName", "EndedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_User" ON "VpnSessionSample"("ServerID", "HubName", "UserName", "StartedDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_ServerStarted" ON "VpnSessionSample"("ServerID", julianday("StartedDate"));
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Session" ON "VpnSessionTrafficSample"("VpnSessionSampleID", "SampledDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Date" ON "VpnSessionTrafficSample"("SampledDate");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Retention" ON "VpnSessionTrafficSample"(datetime("SampledDate"));
CREATE INDEX IF NOT EXISTS "IX_TrafficQuota_Hub" ON "TrafficQuota"("ServerID", "HubName", "SubjectType");
CREATE INDEX IF NOT EXISTS "IX_WireGuardPeer_HubUser" ON "WireGuardPeer"("ServerID", "HubName", "UserName");
CREATE INDEX IF NOT EXISTS "IX_UserTrafficSample_Ts" ON "UserTrafficSample"("ServerID", "HubName", "UserName" COLLATE NOCASE, "SampledTs");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_Ts" ON "HubTrafficSample"("ServerID", "HubName", "SampledTs");
CREATE INDEX IF NOT EXISTS "IX_HubTrafficSample_ServerTs" ON "HubTrafficSample"("ServerID", "SampledTs");
CREATE INDEX IF NOT EXISTS "IX_TrafficRollupHourly_Lookup" ON "TrafficRollupHourly"("ServerID", "SubjectType", "HubName", "UserKey", "BucketTs");
CREATE INDEX IF NOT EXISTS "IX_TrafficRollupHourly_Server" ON "TrafficRollupHourly"("ServerID", "SubjectType", "BucketTs");
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_StartedTs" ON "VpnSessionSample"("ServerID", "StartedTs" DESC, "VpnSessionSampleID" DESC);
CREATE INDEX IF NOT EXISTS "IX_VpnSessionSample_HubStartedTs" ON "VpnSessionSample"("ServerID", "HubName", "StartedTs" DESC, "VpnSessionSampleID" DESC);
CREATE INDEX IF NOT EXISTS "IX_VpnSessionTrafficSample_Ts" ON "VpnSessionTrafficSample"("VpnSessionSampleID", "SampledTs");
CREATE TRIGGER IF NOT EXISTS "TR_VpnSessionSample_StartedTs" AFTER INSERT ON "VpnSessionSample"
WHEN NEW."StartedTs"=0 AND NEW."StartedDate"!=''
BEGIN
    UPDATE "VpnSessionSample" SET "StartedTs"=COALESCE(unixepoch(NEW."StartedDate"), 0) WHERE "VpnSessionSampleID"=NEW."VpnSessionSampleID";
END;
