package release

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

const maxPostgresDatabasePropertiesBytes = 64 << 10

type postgresDatabaseGrant struct {
	GrantorOID  int64  `json:"grantor_oid"`
	GrantorName string `json:"grantor_name"`
	GranteeOID  int64  `json:"grantee_oid"`
	GranteeName string `json:"grantee_name"`
	Privilege   string `json:"privilege"`
	Grantable   bool   `json:"grantable"`
}

type postgresDatabaseSetting struct {
	RoleOID  int64  `json:"role_oid"`
	RoleName string `json:"role_name"`
	Name     string `json:"name"`
	Value    string `json:"value"`
}

type postgresDatabaseLabel struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// These database-level properties are not restored by pg_restore without
// --create. Keep the exact source values with the archive, not a later guess
// from template0 or the live original database.
type postgresDatabaseProperties struct {
	Version                int                       `json:"version"`
	OwnerOID               int64                     `json:"owner_oid"`
	OwnerName              string                    `json:"owner_name"`
	Encoding               string                    `json:"encoding"`
	LocaleProvider         string                    `json:"locale_provider"`
	Collate                string                    `json:"collate"`
	CType                  string                    `json:"ctype"`
	ICULocale              *string                   `json:"icu_locale"`
	ICURules               *string                   `json:"icu_rules"`
	CollationVersion       *string                   `json:"collation_version"`
	ActualCollationVersion *string                   `json:"actual_collation_version"`
	TablespaceOID          int64                     `json:"tablespace_oid"`
	TablespaceName         string                    `json:"tablespace_name"`
	ConnectionLimit        int                       `json:"connection_limit"`
	AllowConnections       bool                      `json:"allow_connections"`
	IsTemplate             bool                      `json:"is_template"`
	DefaultACL             bool                      `json:"default_acl"`
	Grants                 []postgresDatabaseGrant   `json:"grants"`
	Settings               []postgresDatabaseSetting `json:"settings"`
	Comment                *string                   `json:"comment"`
	Labels                 []postgresDatabaseLabel   `json:"labels"`
}

type postgresPropertyQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readPostgresDatabaseProperties(ctx context.Context, q postgresPropertyQuerier, oid int64) (postgresDatabaseProperties, error) {
	var empty postgresDatabaseProperties
	var major int
	if oid <= 0 || q.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('server_version_num')::integer/10000`).Scan(&major) != nil || major != 16 {
		return empty, ErrDatabaseRestore
	}
	var body []byte
	if q.QueryRowContext(ctx, postgresDatabasePropertiesSQL, oid).Scan(&body) != nil || len(body) > maxPostgresDatabasePropertiesBytes {
		return empty, ErrDatabaseRestore
	}
	var properties postgresDatabaseProperties
	if json.Unmarshal(body, &properties) != nil || !properties.valid() {
		return empty, ErrDatabaseRestore
	}
	return properties, nil
}

const postgresDatabasePropertiesSQL = `SELECT pg_catalog.left(pg_catalog.json_build_object(
 'version',1, 'owner_oid',d.datdba::bigint, 'owner_name',r.rolname,
 'encoding',pg_catalog.pg_encoding_to_char(d.encoding), 'locale_provider',d.datlocprovider,
 'collate',d.datcollate, 'ctype',d.datctype, 'icu_locale',d.daticulocale,
 'icu_rules',d.daticurules, 'collation_version',d.datcollversion,
 'actual_collation_version',pg_catalog.pg_database_collation_actual_version(d.oid),
 'tablespace_oid',d.dattablespace::bigint, 'tablespace_name',t.spcname,
 'connection_limit',d.datconnlimit, 'allow_connections',d.datallowconn,
 'is_template',d.datistemplate, 'default_acl',d.datacl IS NULL,
 'comment',pg_catalog.shobj_description(d.oid,'pg_database'),
 'grants',COALESCE((SELECT pg_catalog.json_agg(pg_catalog.json_build_object(
   'grantor_oid',a.grantor::bigint,'grantor_name',pg_catalog.pg_get_userbyid(a.grantor),
   'grantee_oid',a.grantee::bigint,'grantee_name',CASE WHEN a.grantee=0 THEN '' ELSE pg_catalog.pg_get_userbyid(a.grantee) END,
   'privilege',a.privilege_type,'grantable',a.is_grantable)
   ORDER BY a.grantor,a.grantee,a.privilege_type COLLATE pg_catalog."C",a.is_grantable)
   FROM pg_catalog.aclexplode(COALESCE(d.datacl,pg_catalog.acldefault('d',d.datdba))) a),'[]'::pg_catalog.json),
 'settings',COALESCE((SELECT pg_catalog.json_agg(pg_catalog.json_build_object(
   'role_oid',s.setrole::bigint,'role_name',CASE WHEN s.setrole=0 THEN '' ELSE pg_catalog.pg_get_userbyid(s.setrole) END,
   'name',pg_catalog.split_part(v.setting,'=',1),'value',pg_catalog.substr(v.setting,pg_catalog.strpos(v.setting,'=')+1))
   ORDER BY s.setrole,pg_catalog.split_part(v.setting,'=',1) COLLATE pg_catalog."C")
   FROM pg_catalog.pg_db_role_setting s CROSS JOIN LATERAL pg_catalog.unnest(s.setconfig) v(setting)
   WHERE s.setdatabase=d.oid),'[]'::pg_catalog.json),
 'labels',COALESCE((SELECT pg_catalog.json_agg(pg_catalog.json_build_object('provider',l.provider,'label',l.label) ORDER BY l.provider COLLATE pg_catalog."C")
   FROM pg_catalog.pg_shseclabel l WHERE l.objoid=d.oid AND l.classoid='pg_catalog.pg_database'::pg_catalog.regclass),'[]'::pg_catalog.json)
 )::text,65537)
 FROM pg_catalog.pg_database d JOIN pg_catalog.pg_roles r ON r.oid=d.datdba
 JOIN pg_catalog.pg_tablespace t ON t.oid=d.dattablespace WHERE d.oid=$1`

func validPostgresPropertyText(value string, maximum int) bool {
	return len(value) <= maximum && !strings.ContainsRune(value, 0)
}

func (p postgresDatabaseProperties) valid() bool {
	if p.Version != 1 || p.OwnerOID <= 0 || p.OwnerName == "" || !validPostgresPropertyText(p.OwnerName, 63) ||
		p.Encoding == "" || !validPostgresPropertyText(p.Encoding, 63) || (p.LocaleProvider != "c" && p.LocaleProvider != "i") ||
		p.Collate == "" || p.CType == "" || !validPostgresPropertyText(p.Collate, 4096) || !validPostgresPropertyText(p.CType, 4096) ||
		p.TablespaceOID <= 0 || p.TablespaceName == "" || !validPostgresPropertyText(p.TablespaceName, 63) ||
		p.ConnectionLimit < -2 || p.Grants == nil || p.Settings == nil || p.Labels == nil {
		return false
	}
	for _, value := range []*string{p.ICULocale, p.ICURules, p.CollationVersion, p.ActualCollationVersion, p.Comment} {
		if value != nil && !validPostgresPropertyText(*value, maxPostgresDatabasePropertiesBytes) {
			return false
		}
	}
	if (p.LocaleProvider == "c" && (p.ICULocale != nil || p.ICURules != nil)) ||
		(p.LocaleProvider == "i" && (p.ICULocale == nil || *p.ICULocale == "")) {
		return false
	}
	roles := map[int64]string{p.OwnerOID: p.OwnerName}
	addRole := func(oid int64, name string) bool {
		if oid == 0 {
			return name == ""
		}
		if oid < 0 || name == "" || !validPostgresPropertyText(name, 63) {
			return false
		}
		if old, ok := roles[oid]; ok && old != name {
			return false
		}
		roles[oid] = name
		return true
	}
	seen := make(map[string]bool)
	for _, grant := range p.Grants {
		key := strconv.FormatInt(grant.GrantorOID, 10) + ":" + strconv.FormatInt(grant.GranteeOID, 10) + ":" + grant.Privilege
		if grant.GrantorOID == 0 || !addRole(grant.GrantorOID, grant.GrantorName) || !addRole(grant.GranteeOID, grant.GranteeName) ||
			(grant.Privilege != "CONNECT" && grant.Privilege != "CREATE" && grant.Privilege != "TEMPORARY") ||
			(grant.GranteeOID == 0 && grant.Grantable) || seen[key] {
			return false
		}
		seen[key] = true
	}
	clear(seen)
	for _, setting := range p.Settings {
		key := strconv.FormatInt(setting.RoleOID, 10) + ":" + setting.Name
		if !addRole(setting.RoleOID, setting.RoleName) || setting.Name == "" || strings.Contains(setting.Name, "=") ||
			!validPostgresPropertyText(setting.Name, 1024) || !validPostgresPropertyText(setting.Value, maxPostgresDatabasePropertiesBytes) || seen[key] {
			return false
		}
		seen[key] = true
	}
	clear(seen)
	for _, label := range p.Labels {
		if label.Provider == "" || !validPostgresPropertyText(label.Provider, 63) ||
			!validPostgresPropertyText(label.Label, maxPostgresDatabasePropertiesBytes) || seen[label.Provider] {
			return false
		}
		seen[label.Provider] = true
	}
	body, _ := json.Marshal(p)
	return len(body) <= maxPostgresDatabasePropertiesBytes
}

func (p postgresDatabaseProperties) same(other postgresDatabaseProperties) bool {
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(other)
	return bytes.Equal(a, b)
}

func (p postgresDatabaseProperties) createSQL(name string) (string, error) {
	if !p.valid() || !postgresRestoreDatabaseName.MatchString(name) || !p.AllowConnections || p.IsTemplate ||
		p.ConnectionLimit < -1 || !samePostgresOptionalString(p.CollationVersion, p.ActualCollationVersion) {
		return "", ErrDatabaseRestore
	}
	provider := "libc"
	if p.LocaleProvider == "i" {
		provider = "icu"
	}
	query := `CREATE DATABASE ` + pq.QuoteIdentifier(name) + ` WITH TEMPLATE template0 OWNER ` + pq.QuoteIdentifier(p.OwnerName) +
		` ENCODING ` + pq.QuoteLiteral(p.Encoding) + ` LOCALE_PROVIDER ` + pq.QuoteLiteral(provider) +
		` LC_COLLATE ` + pq.QuoteLiteral(p.Collate) + ` LC_CTYPE ` + pq.QuoteLiteral(p.CType) +
		` TABLESPACE ` + pq.QuoteIdentifier(p.TablespaceName) + ` CONNECTION LIMIT ` + strconv.Itoa(p.ConnectionLimit) +
		` ALLOW_CONNECTIONS true IS_TEMPLATE false`
	if p.ICULocale != nil {
		query += ` ICU_LOCALE ` + pq.QuoteLiteral(*p.ICULocale)
	}
	if p.ICURules != nil {
		query += ` ICU_RULES ` + pq.QuoteLiteral(*p.ICURules)
	}
	return query, nil
}

func samePostgresOptionalString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// Validate every referenced role before creating a target. No role, membership,
// global permission or tablespace is created or altered by this workflow.
func (p postgresDatabaseProperties) checkCreationPrivileges(ctx context.Context, q postgresPropertyQuerier, sourceOID int64) error {
	if !p.valid() {
		return ErrDatabaseRestore
	}
	var currentOID int64
	var createdb, super, connect, tablespace bool
	if q.QueryRowContext(ctx, `SELECT r.oid::bigint,r.rolcreatedb,r.rolsuper,
	 pg_catalog.has_database_privilege($1::oid,'CONNECT'),pg_catalog.has_tablespace_privilege($2::oid,'CREATE')
	 FROM pg_catalog.pg_roles r WHERE r.rolname=current_user`, sourceOID, p.TablespaceOID).
		Scan(&currentOID, &createdb, &super, &connect, &tablespace) != nil || (!createdb && !super) || !connect || !tablespace ||
		(!super && p.ConnectionLimit >= 0 && p.ConnectionLimit < 2) || (!super && len(p.Labels) != 0) {
		return ErrDatabaseRestore
	}
	roles := map[int64]string{p.OwnerOID: p.OwnerName}
	setRole := map[int64]bool{p.OwnerOID: true}
	for _, grant := range p.Grants {
		roles[grant.GrantorOID] = grant.GrantorName
		setRole[grant.GrantorOID] = true
		if grant.GranteeOID != 0 {
			roles[grant.GranteeOID] = grant.GranteeName
		}
	}
	for _, setting := range p.Settings {
		if !(postgresSettingListQuoted(setting.Name) && setting.Value == "") {
			if _, err := postgresSettingSQLValue(setting); err != nil {
				return ErrDatabaseRestore
			}
		}
		if setting.RoleOID != 0 {
			roles[setting.RoleOID] = setting.RoleName
			if !super && setting.RoleOID != currentOID {
				return ErrDatabaseRestore
			}
		}
	}
	for oid, name := range roles {
		var actual string
		var canSet bool
		if q.QueryRowContext(ctx, `SELECT rolname,pg_catalog.pg_has_role(oid,'SET') FROM pg_catalog.pg_roles WHERE oid=$1`, oid).
			Scan(&actual, &canSet) != nil || actual != name || (setRole[oid] && !canSet) {
			return ErrDatabaseRestore
		}
	}
	_, err := p.orderedGrants()
	return err
}

func (p postgresDatabaseProperties) orderedGrants() ([]postgresDatabaseGrant, error) {
	remaining := append([]postgresDatabaseGrant(nil), p.Grants...)
	result := make([]postgresDatabaseGrant, 0, len(remaining))
	available := map[int64]map[string]bool{p.OwnerOID: {"CONNECT": true, "CREATE": true, "TEMPORARY": true}}
	for len(remaining) > 0 {
		next := make([]postgresDatabaseGrant, 0, len(remaining))
		for _, grant := range remaining {
			if !available[grant.GrantorOID][grant.Privilege] {
				next = append(next, grant)
				continue
			}
			result = append(result, grant)
			if grant.Grantable {
				if available[grant.GranteeOID] == nil {
					available[grant.GranteeOID] = make(map[string]bool)
				}
				available[grant.GranteeOID][grant.Privilege] = true
			}
		}
		if len(next) == len(remaining) {
			return nil, ErrDatabaseRestore
		}
		remaining = next
	}
	return result, nil
}

func (p postgresDatabaseProperties) apply(ctx context.Context, conn *sql.Conn, name string) error {
	if !p.valid() || !postgresRestoreDatabaseName.MatchString(name) {
		return ErrDatabaseRestore
	}
	grants, err := p.orderedGrants()
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return ErrDatabaseRestore
	}
	defer tx.Rollback()
	setRole := func(role string) error {
		_, err := tx.ExecContext(ctx, `SET LOCAL ROLE `+pq.QuoteIdentifier(role))
		return err
	}
	if setRole(p.OwnerName) != nil {
		return ErrDatabaseRestore
	}
	target := pq.QuoteIdentifier(name)
	if !p.DefaultACL {
		if _, err := tx.ExecContext(ctx, `REVOKE ALL ON DATABASE `+target+` FROM PUBLIC, `+pq.QuoteIdentifier(p.OwnerName)); err != nil {
			return ErrDatabaseRestore
		}
		for _, grant := range grants {
			if setRole(grant.GrantorName) != nil {
				return ErrDatabaseRestore
			}
			grantee := "PUBLIC"
			if grant.GranteeOID != 0 {
				grantee = pq.QuoteIdentifier(grant.GranteeName)
			}
			query := `GRANT ` + grant.Privilege + ` ON DATABASE ` + target + ` TO ` + grantee
			if grant.Grantable {
				query += ` WITH GRANT OPTION`
			}
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return ErrDatabaseRestore
			}
		}
	}
	if setRole(p.OwnerName) != nil {
		return ErrDatabaseRestore
	}
	if p.Comment != nil {
		if _, err := tx.ExecContext(ctx, `COMMENT ON DATABASE `+target+` IS `+pq.QuoteLiteral(*p.Comment)); err != nil {
			return ErrDatabaseRestore
		}
	}
	// Role-specific defaults are changed only for this new database. RESET ROLE
	// returns to the original privileged session for ALTER ROLE/security labels.
	if _, err := tx.ExecContext(ctx, `RESET ROLE`); err != nil {
		return ErrDatabaseRestore
	}
	for _, setting := range p.Settings {
		query := `ALTER DATABASE ` + target
		if setting.RoleOID != 0 {
			if _, err := tx.ExecContext(ctx, `RESET ROLE`); err != nil {
				return ErrDatabaseRestore
			}
			query = `ALTER ROLE ` + pq.QuoteIdentifier(setting.RoleName) + ` IN DATABASE ` + target
		} else if setRole(p.OwnerName) != nil {
			return ErrDatabaseRestore
		}
		query += ` SET ` + pq.QuoteIdentifier(setting.Name)
		if postgresSettingListQuoted(setting.Name) && setting.Value == "" {
			if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.set_config($1,'',true)`, setting.Name); err != nil {
				return ErrDatabaseRestore
			}
			query += ` FROM CURRENT`
		} else {
			value, err := postgresSettingSQLValue(setting)
			if err != nil {
				return ErrDatabaseRestore
			}
			query += ` TO ` + value
		}
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return ErrDatabaseRestore
		}
	}
	if _, err := tx.ExecContext(ctx, `RESET ROLE`); err != nil {
		return ErrDatabaseRestore
	}
	for _, label := range p.Labels {
		if _, err := tx.ExecContext(ctx, `SECURITY LABEL FOR `+pq.QuoteIdentifier(label.Provider)+` ON DATABASE `+target+` IS `+pq.QuoteLiteral(label.Label)); err != nil {
			return ErrDatabaseRestore
		}
	}
	return tx.Commit()
}

// PostgreSQL 16's dump utility distinguishes these GUC_LIST_QUOTE settings.
// Their stored values are already identifier lists; quoting the whole list as
// one string would turn search_path entries into one different schema name.
func postgresSettingListQuoted(name string) bool {
	switch strings.ToLower(name) {
	case "local_preload_libraries", "output_plugin_libraries", "search_path", "session_preload_libraries",
		"shared_preload_libraries", "temp_tablespaces", "unix_socket_directories":
		return true
	}
	return false
}

func postgresSettingSQLValue(setting postgresDatabaseSetting) (string, error) {
	if !postgresSettingListQuoted(setting.Name) {
		return pq.QuoteLiteral(setting.Value), nil
	}
	// encoding/csv folds CRLF even inside quoted fields. Double CR before LF
	// so its normalization preserves the exact catalog identifier bytes.
	reader := csv.NewReader(strings.NewReader(strings.ReplaceAll(setting.Value, "\r\n", "\r\r\n")))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	values, err := reader.Read()
	if err != nil || len(values) == 0 {
		return "", ErrDatabaseRestore
	}
	if _, err := reader.Read(); err != io.EOF {
		return "", ErrDatabaseRestore
	}
	for i, value := range values {
		values[i] = pq.QuoteLiteral(value)
	}
	return strings.Join(values, ", "), nil
}
