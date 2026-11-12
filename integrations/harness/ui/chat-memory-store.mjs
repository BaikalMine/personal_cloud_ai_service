import { constants, closeSync, fstatSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync, readdirSync, unlinkSync } from 'node:fs';
import { createHash, randomUUID } from 'node:crypto';
import { isAbsolute, join } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { compileMemoryQuery, scoreMemoryRecord } from './chat-memory-search.mjs';
import { normalizeMemorySources } from './chat-memory-sources.mjs';
import { initMemoryReview, reviewReserve, reviewSelection, prepareMemoryReview, commitMemoryReview, undoMemoryReview } from './chat-memory-review-store.mjs';

const MAX_RECORD_BYTES = 16 * 1024;
const MAX_LEGACY_BYTES = 1024 * 1024;
const MAX_ACCOUNT_BYTES = 50 * 1024 * 1024;
const MAX_QUERY_BYTES = 4096;
const STATUSES = new Set(['normal', 'important', 'critical']);
const RELATIONS = new Set(['related', 'supports', 'contradicts', 'depends_on', 'updates', 'part_of']);
const RECORD_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const SESSION_DIR = /^[0-9a-f]{64}$/;
const deleting = () => globalThis[Symbol.for('ai-gateway.archiveDeleting')];

export const isChatMemoryStatus = value => STATUSES.has(value);

function validString(value, name, max = 4096) {
  if (typeof value !== 'string' || !value.trim() || Buffer.byteLength(value) > max) throw new Error(`invalid_${name}`);
}

function validId(id) {
  if (typeof id !== 'string' || !RECORD_ID.test(id)) throw new Error('invalid_id');
}

function validRevision(revision) {
  if (revision !== undefined && (typeof revision !== 'string' || !/^(0|[1-9][0-9]*)$/.test(revision))) throw new Error('invalid_revision');
}

function readAliases(options) {
  if (options === undefined) return undefined;
  if (!options || typeof options !== 'object' || Array.isArray(options)) throw new Error('invalid_aliases');
  if (options.aliases === undefined) return undefined;
  return parseAliases(options.aliases, 'aliases');
}

function parseAliases(values, name, trim = true) {
  if (!Array.isArray(values) || values.length > 12) throw new Error(`invalid_${name}`);
  const aliases = [], seen = new Set();
  let total = 0;
  for (const value of values) {
    if (typeof value !== 'string') throw new Error(`invalid_${name}`);
    const alias = trim ? value.trim() : value, bytes = Buffer.byteLength(alias);
    if (!alias.trim() || bytes > 80 || seen.has(alias)) throw new Error(`invalid_${name}`);
    total += bytes;
    if (total > 1024) throw new Error(`invalid_${name}`);
    seen.add(alias);
    aliases.push(alias);
  }
  return aliases;
}

function sameAliases(left = [], right = []) {
  return left.length === right.length && left.every((alias, index) => alias === right[index]);
}

function readSources(options, key = 'sources') {
  if (key === 'sources' && options?.sources === undefined) return undefined;
  try { return normalizeMemorySources(options?.[key]); }
  catch (error) {
    if (key === 'expectedSources' && error.message === 'invalid_sources') throw new Error('invalid_expected_sources', { cause: error });
    throw error;
  }
}

function sameSources(left = [], right = []) {
  return JSON.stringify(left) === JSON.stringify(right);
}

function snapshot(db, session, expectedRevision, operation) {
  db.exec('BEGIN');
  try {
    const revision = readRevision(db, session);
    if (expectedRevision !== undefined && revision !== expectedRevision) throw new Error('chat_memory_conflict');
    const result = operation(revision);
    db.exec('COMMIT');
    return result;
  } catch (error) { db.exec('ROLLBACK'); throw error; }
}

function storageUsage(db, session) {
  const where = session === undefined ? '' : 'WHERE session = ?';
  const params = session === undefined ? [] : [session, session];
  const row = db.prepare(`SELECT facts.n AS facts, links.n AS links, facts.bytes + links.bytes AS bytes
    FROM (SELECT COUNT(*) AS n, COALESCE(SUM(bytes + 1), 0) + COUNT(DISTINCT session) AS bytes FROM records ${where}) facts
    CROSS JOIN (SELECT COUNT(*) AS n, COALESCE(SUM(bytes + 1), 0) + COUNT(DISTINCT session) AS bytes FROM relations ${where}) links`).get(...params);
  return {facts:row.facts,links:row.links,bytes:row.bytes};
}

const accountUsage = db => storageUsage(db).bytes + reviewReserve(db);

function previewNode(record) {
  const points = Array.from(record.text);
  return { id: record.id, preview: points.slice(0, 240).join(''), truncated: points.length > 240 };
}

function privateDirectory(path, create = false) {
  if (create) {
    try { mkdirSync(path, { mode: 0o700 }); } catch (err) { if (err.code !== 'EEXIST') throw err; }
  }
  const stat = lstatSync(path);
  if (!stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== process.getuid() || (stat.mode & 0o077)) {
    throw new Error('chat_memory_insecure_directory');
  }
}

function privateFile(fd) {
  const stat = fstatSync(fd);
  if (!stat.isFile() || stat.nlink !== 1 || stat.uid !== process.getuid() || (stat.mode & 0o077)) {
    throw new Error('chat_memory_invalid_file');
  }
  return stat;
}

function readLegacy(path) {
  let fd;
  try {
    fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    if (privateFile(fd).size > MAX_LEGACY_BYTES) throw new Error('chat_memory_invalid_file');
    const data = readFileSync(fd);
    if (data.length > MAX_LEGACY_BYTES) throw new Error('chat_memory_invalid_file');
    let records;
    try { records = JSON.parse(data.toString('utf8')); } catch { throw new Error('chat_memory_invalid_file'); }
    if (!Array.isArray(records) || records.length > 1000 || records.some(record =>
      !record || typeof record !== 'object' || typeof record.id !== 'string' || !RECORD_ID.test(record.id) ||
      typeof record.text !== 'string' || !record.text.trim() || Buffer.byteLength(record.text) > MAX_RECORD_BYTES ||
      typeof record.createdAt !== 'string' || !Number.isFinite(Date.parse(record.createdAt))
    ) || new Set(records.map(record => record.id)).size !== records.length) throw new Error('chat_memory_invalid_file');
    return records;
  } catch (err) {
    if (err.code === 'ENOENT') return [];
    throw err;
  } finally { if (fd !== undefined) closeSync(fd); }
}

function openDatabase(root, timeout) {
  const path = join(root, 'records.sqlite');
  let fd;
  let created = false;
  try {
    try { fd = openSync(path, constants.O_CREAT | constants.O_EXCL | constants.O_RDWR | constants.O_NOFOLLOW, 0o600); created = true; }
    catch (err) {
      if (err.code !== 'EEXIST') throw err;
      fd = openSync(path, constants.O_RDWR | constants.O_NOFOLLOW);
    }
    privateFile(fd);
    if (created) {
      fsyncSync(fd);
      const directory = openSync(root, constants.O_RDONLY);
      try { fsyncSync(directory); } finally { closeSync(directory); }
    }
  } finally { if (fd !== undefined) closeSync(fd); }
  const db = new DatabaseSync(path, { timeout });
  try {
    db.exec('PRAGMA foreign_keys=ON; PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; BEGIN IMMEDIATE');
    try {
      db.exec('CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS records (seq INTEGER PRIMARY KEY, session TEXT NOT NULL, id TEXT NOT NULL, json TEXT NOT NULL, bytes INTEGER NOT NULL, UNIQUE(session, id)); CREATE INDEX IF NOT EXISTS records_session ON records(session, seq)');
      db.exec(`CREATE TABLE IF NOT EXISTS relations (
        seq INTEGER PRIMARY KEY,
        session TEXT NOT NULL,
        id TEXT NOT NULL,
        from_id TEXT NOT NULL,
        to_id TEXT NOT NULL,
        relation TEXT NOT NULL CHECK(relation IN ('related', 'supports', 'contradicts', 'depends_on', 'updates', 'part_of')),
        json TEXT NOT NULL,
        bytes INTEGER NOT NULL,
        UNIQUE(session, id),
        UNIQUE(session, from_id, to_id, relation),
        CHECK(from_id <> to_id),
        CHECK(relation <> 'related' OR from_id < to_id),
        FOREIGN KEY(session, from_id) REFERENCES records(session, id),
        FOREIGN KEY(session, to_id) REFERENCES records(session, id)
      );
      CREATE INDEX IF NOT EXISTS relations_from ON relations(session, from_id, seq);
      CREATE INDEX IF NOT EXISTS relations_to ON relations(session, to_id, seq)`);
      initMemoryReview(db);
      if (!db.prepare("SELECT 1 FROM metadata WHERE key = 'legacy_imported'").get()) {
        const insert = db.prepare('INSERT INTO records(session, id, json, bytes) VALUES (?, ?, ?, ?)');
        for (const name of readdirSync(root).filter(name => SESSION_DIR.test(name)).sort()) {
          const dir = join(root, name);
          privateDirectory(dir);
          for (const record of readLegacy(join(dir, 'records.json'))) {
            const json = JSON.stringify(record);
            insert.run(name, record.id, json, Buffer.byteLength(json));
          }
        }
        db.prepare("INSERT INTO metadata VALUES ('legacy_imported', '1')").run();
      }
      db.exec('COMMIT');
    } catch (err) { db.exec('ROLLBACK'); throw err; }
    return db;
  } catch (err) { db.close(); throw err; }
}

const revisionKey = session => 'revision:' + session;
const readRevision = (db, session) => db.prepare('SELECT value FROM metadata WHERE key = ?').get(revisionKey(session))?.value ?? '0';
function bumpRevision(db, session) {
  db.prepare("INSERT INTO metadata(key, value) VALUES (?, '1') ON CONFLICT(key) DO UPDATE SET value = CAST(metadata.value AS INTEGER) + 1").run(revisionKey(session));
}

function writeUpdatedRecord(db, session, id, record, previousBytes) {
  const json = JSON.stringify(record), bytes = Buffer.byteLength(json);
  if (accountUsage(db) - previousBytes + bytes > MAX_ACCOUNT_BYTES) throw new Error('chat_memory_limit');
  db.prepare('UPDATE records SET json = ?, bytes = ? WHERE session = ? AND id = ?').run(json, bytes, session, id);
  bumpRevision(db, session);
}

const sessionKey = id => createHash('sha256').update(id).digest('hex');
const transferTables = [
  ['facts', 'records', 'id, json, bytes', ''],
  ['links', 'relations', 'id, from_id, to_id, relation, json, bytes',
    'AND NOT EXISTS (SELECT 1 FROM relations dst WHERE dst.session = ? AND dst.from_id = src.from_id AND dst.to_id = src.to_id AND dst.relation = src.relation)'],
];

function transferMemory(db, source, target, expected) {
  db.exec(expected ? 'BEGIN IMMEDIATE' : 'BEGIN');
  try {
    const sourceRevision = readRevision(db, source), targetRevision = readRevision(db, target);
    if (expected && (expected.sourceRevision !== sourceRevision || expected.targetRevision !== targetRevision)) {
      throw new Error('chat_memory_conflict');
    }
    const result = { sourceRevision, targetRevision };
    let bytes = 0;
    const inserts = [];
    // Keep IDs so copying again (even via a third chat) never overwrites local edits.
    for (const [key, table, columns, linkMatch] of transferTables) {
      const missing = `FROM ${table} src WHERE src.session = ?
        AND NOT EXISTS (SELECT 1 FROM ${table} dst WHERE dst.session = ? AND dst.id = src.id) ${linkMatch}`;
      const args = linkMatch ? [source, target, target] : [source, target];
      const {count, size} = db.prepare(`SELECT COUNT(*) AS count, COALESCE(SUM(src.bytes + 1),0) AS size ${missing}`).get(...args);
      const {total} = db.prepare(`SELECT COUNT(*) AS total FROM ${table} WHERE session = ?`).get(source);
      result[key] = { added:count, skipped:total-count };
      if (count) {
        bytes += size + (db.prepare(`SELECT 1 FROM ${table} WHERE session = ? LIMIT 1`).get(target) ? 0 : 1);
        inserts.push([`INSERT INTO ${table}(session, ${columns}) SELECT ?, ${columns} ${missing} ORDER BY src.seq`, args]);
      }
    }
    result.fits = !bytes || accountUsage(db) + bytes <= MAX_ACCOUNT_BYTES;
    if (expected) {
      if (!result.fits) throw new Error('chat_memory_limit');
      for (const [sql,args] of inserts) db.prepare(sql).run(target, ...args);
      if (inserts.length) bumpRevision(db, target);
      result.targetRevision = readRevision(db, target);
    }
    db.exec('COMMIT');
    return result;
  } catch (error) { db.exec('ROLLBACK'); throw error; }
}

function readPage(db, session, cursor, limit, query) {
  if (query) {
    const plan = compileMemoryQuery(query, [], { maxQueryBytes: 4096, maxTerms: 256 }), matches = [];
    for (const row of db.prepare('SELECT json, seq FROM records WHERE session = ? ORDER BY seq').iterate(session)) {
      const record = JSON.parse(row.json);
      let score = scoreMemoryRecord(record.text, plan);
      for (const alias of record.aliases ?? []) score = Math.max(score, scoreMemoryRecord(alias, plan));
      if (score > 0) matches.push({ record, score, seq: row.seq });
    }
    matches.sort((a, b) => b.score - a.score || a.seq - b.seq);
    return { records: matches.slice(cursor, cursor + limit).map(match => match.record), nextCursor: cursor + limit < matches.length ? cursor + limit : null };
  }
  const rows = db.prepare('SELECT json FROM records WHERE session = ? ORDER BY seq LIMIT ? OFFSET ?').all(session, limit + 1, cursor);
  return { records:rows.slice(0, limit).map(row => JSON.parse(row.json)), nextCursor:rows.length > limit ? cursor + limit : null };
}

// The limit counts UTF-8 record and relationship JSON arrays, not SQLite overhead.
export function createChatMemoryStore({ dshHome, lockTimeoutMs = 5000 } = {}) {
  if (typeof dshHome !== 'string' || !isAbsolute(dshHome)) throw new Error('invalid_dsh_home');
  if (!Number.isSafeInteger(lockTimeoutMs) || lockTimeoutMs < 0 || lockTimeoutMs > 60000) throw new Error('invalid_lock_timeout');

  async function run(sessionId, operation, timeout = lockTimeoutMs, purge = false) {
    validString(sessionId, 'session_id');
    if (!purge && deleting()?.has(sessionId)) throw new Error('chat_memory_unavailable');
    if (process.platform === 'win32' || typeof process.getuid !== 'function') throw new Error('chat_memory_private_permissions_unsupported');
    privateDirectory(dshHome);
    const root = join(dshHome, 'chat-memory');
    privateDirectory(root, true);
    let db;
    try {
      db = openDatabase(root, timeout);
      return operation(db, sessionKey(sessionId));
    } catch (err) {
      if (err.code === 'ERR_SQLITE_ERROR' && /(?:database is locked|database is busy)/i.test(err.message)) throw new Error('chat_memory_lock_timeout', { cause: err });
      throw err;
    } finally { db?.close(); }
  }

  return {
    async deleteSession(sessionId) {
      return run(sessionId, (db, session) => {
        const legacy = join(dshHome, 'chat-memory', session);
        const path = join(legacy, 'records.json');
        const hasLegacy = lstatSync(legacy, { throwIfNoEntry:false }) !== undefined;
        if (hasLegacy) {
          privateDirectory(legacy);
          readLegacy(path);
        }
        db.exec('PRAGMA secure_delete=ON; BEGIN IMMEDIATE');
        try {
          db.prepare('DELETE FROM relations WHERE session = ?').run(session);
          db.prepare('DELETE FROM records WHERE session = ?').run(session);
          db.prepare('DELETE FROM metadata WHERE key = ?').run(revisionKey(session));
          db.prepare('DELETE FROM memory_reviews WHERE session = ?').run(session);
          db.exec('COMMIT');
        } catch (error) { db.exec('ROLLBACK'); throw error; }
        if (hasLegacy) {
          try { unlinkSync(path); } catch (error) { if (error.code !== 'ENOENT') throw error; }
          const fd = openSync(legacy, 'r');
          try { fsyncSync(fd); } finally { closeSync(fd); }
        }
      }, lockTimeoutMs, true);
    },
    async transferPreview(sourceId, targetId) {
      validString(targetId, 'session_id');
      if (deleting()?.has(targetId)) throw new Error('chat_memory_unavailable');
      if (sourceId === targetId) throw new Error('invalid_target_session');
      return run(sourceId, (db, source) => transferMemory(db, source, sessionKey(targetId)));
    },
    async transfer(sourceId, targetId, sourceRevision, targetRevision) {
      validString(targetId, 'session_id');
      if (deleting()?.has(targetId)) throw new Error('chat_memory_unavailable');
      if (sourceId === targetId) throw new Error('invalid_target_session');
      if (sourceRevision === undefined || targetRevision === undefined) throw new Error('invalid_revision');
      validRevision(sourceRevision);
      validRevision(targetRevision);
      return run(sourceId, (db, source) => transferMemory(db, source, sessionKey(targetId), {sourceRevision, targetRevision}));
    },
    async status(sessionId) {
      return run(sessionId,(db,session)=>snapshot(db,session,undefined,revision=>({revision,
        facts:db.prepare('SELECT COUNT(*) AS total FROM records WHERE session = ?').get(session).total,
        links:db.prepare('SELECT COUNT(*) AS total FROM relations WHERE session = ?').get(session).total,
      })),Math.min(lockTimeoutMs,50));
    },
    async usage(sessionId) {
      return run(sessionId, (db, session) => snapshot(db, session, undefined, () => {
        const chat=storageUsage(db, session), profile=storageUsage(db);
        const reservedBytes=reviewReserve(db);
        return {chat, profile, reservedBytes, remainingBytes:Math.max(0,MAX_ACCOUNT_BYTES-profile.bytes-reservedBytes), limitBytes:MAX_ACCOUNT_BYTES};
      }));
    },
    async reviewSelection(sessionId, ids) {
      return run(sessionId,(db,session)=>snapshot(db,session,undefined,revision=>({revision,...reviewSelection(db,session,ids)})));
    },
    async reviewPreview(sessionId, revision, ids, proposal) {
      return run(sessionId,(db,session)=>snapshot(db,session,revision,()=>prepareMemoryReview(db,session,ids,proposal)));
    },
    async reviewApply(sessionId, id, revision, patch) {
      return run(sessionId,(db,session)=>{
        db.exec('BEGIN IMMEDIATE');
        try{
          const existing=db.prepare('SELECT state,expires FROM memory_reviews WHERE session=? AND id=?').get(session,id);
          if(!existing&&readRevision(db,session)!==revision)throw new Error('review_stale');
          const result=commitMemoryReview(db,session,id,patch,MAX_ACCOUNT_BYTES,()=>accountUsage(db));
          if(result.delta!==undefined)bumpRevision(db,session);
          db.exec('COMMIT');return result;
        }catch(error){db.exec('ROLLBACK');throw error;}
      });
    },
    async reviewUndo(sessionId,id) {
      return run(sessionId,(db,session)=>{
        db.exec('BEGIN IMMEDIATE');
        try{
          const result=undoMemoryReview(db,session,id,MAX_ACCOUNT_BYTES,()=>accountUsage(db));
          if(result.changed)bumpRevision(db,session);
          db.exec('COMMIT');return {id,state:result.state};
        }catch(error){db.exec('ROLLBACK');throw error;}
      });
    },
    async reviewHistory(sessionId) {
      return run(sessionId,(db,session)=>db.prepare('SELECT id, created, expires, state, reserve FROM memory_reviews WHERE session=? ORDER BY created DESC LIMIT 50').all(session));
    },
    async add(sessionId, text, requestId, options) {
      validString(text, 'text', MAX_RECORD_BYTES);
      if (requestId !== undefined && (typeof requestId !== 'string' || !RECORD_ID.test(requestId))) throw new Error('invalid_id');
      const aliases = readAliases(options) ?? [];
      const sources = readSources(options) ?? [];
      const status = options?.status === undefined ? 'normal' : options.status;
      if (!isChatMemoryStatus(status)) throw new Error('invalid_status');
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          if (requestId !== undefined) {
            const existing = db.prepare('SELECT json FROM records WHERE session = ? AND id = ?').get(session, requestId);
            if (existing) {
              const record = JSON.parse(existing.json);
              if (record.text !== text || (record.status??'normal') !== status || !sameAliases(record.aliases, aliases) || !sameSources(record.sources, sources)) throw new Error('chat_memory_conflict');
              db.exec('COMMIT');
              return record;
            }
          }
          const record = { id: requestId ?? randomUUID(), text, createdAt: new Date().toISOString() };
          if (aliases.length) record.aliases = aliases;
          if (sources.length) record.sources = sources;
          if (status !== 'normal') record.status = status;
          const json = JSON.stringify(record);
          const usage = accountUsage(db);
          const exists = db.prepare('SELECT 1 FROM records WHERE session = ? LIMIT 1').get(session);
          if (usage + Buffer.byteLength(json) + (exists ? 1 : 2) > MAX_ACCOUNT_BYTES) throw new Error('chat_memory_limit');
          db.prepare('INSERT INTO records(session, id, json, bytes) VALUES (?, ?, ?, ?)').run(session, record.id, json, Buffer.byteLength(json));
          bumpRevision(db, session);
          db.exec('COMMIT');
          return record;
        } catch (err) { db.exec('ROLLBACK'); throw err; }
      });
    },
    async list(sessionId) { return run(sessionId, (db, session) => db.prepare('SELECT json FROM records WHERE session = ? ORDER BY seq').all(session).map(row => JSON.parse(row.json))); },
    async get(sessionId, id, expectedRevision) {
      validId(id);
      validRevision(expectedRevision);
      return run(sessionId, (db, session) => snapshot(db, session, expectedRevision, () => {
        const row = db.prepare('SELECT json FROM records WHERE session = ? AND id = ?').get(session, id);
        return row ? JSON.parse(row.json) : null;
      }));
    },
    async scan(sessionId, visitor, expectedRevision) {
      if (typeof visitor !== 'function') throw new Error('invalid_visitor');
      validRevision(expectedRevision);
      return run(sessionId, (db, session) => snapshot(db, session, expectedRevision, revision => {
        const total = db.prepare('SELECT COUNT(*) AS total FROM records WHERE session = ?').get(session).total;
        for (const row of db.prepare('SELECT json, seq FROM records WHERE session = ? ORDER BY seq').iterate(session)) {
          const result = visitor(JSON.parse(row.json), row.seq);
          if (result && typeof result.then === 'function') throw new Error('invalid_visitor');
          if (result === false) break;
        }
        return { revision, total };
      }));
    },
    async link(sessionId, fromId, toId, relation = 'related', note = '') {
      validId(fromId);
      validId(toId);
      if (fromId === toId) throw new Error('invalid_link');
      if (!RELATIONS.has(relation)) throw new Error('invalid_relation');
      if (typeof note !== 'string' || Buffer.byteLength(note) > 256) throw new Error('invalid_note');
      if (relation === 'related' && fromId > toId) [fromId, toId] = [toId, fromId];
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          const endpoints = db.prepare('SELECT COUNT(*) AS total FROM records WHERE session = ? AND id IN (?, ?)').get(session, fromId, toId).total;
          if (endpoints !== 2) throw new Error('chat_memory_conflict');
          const existing = db.prepare('SELECT json FROM relations WHERE session = ? AND from_id = ? AND to_id = ? AND relation = ?').get(session, fromId, toId, relation);
          if (existing) {
            const edge = JSON.parse(existing.json);
            if (edge.note !== note) throw new Error('chat_memory_conflict');
            db.exec('COMMIT');
            return edge;
          }
          const edge = { id: randomUUID(), fromId, toId, relation, note, createdAt: new Date().toISOString() };
          const json = JSON.stringify(edge), bytes = Buffer.byteLength(json);
          const exists = db.prepare('SELECT 1 FROM relations WHERE session = ? LIMIT 1').get(session);
          if (accountUsage(db) + bytes + (exists ? 1 : 2) > MAX_ACCOUNT_BYTES) throw new Error('chat_memory_limit');
          db.prepare('INSERT INTO relations(session, id, from_id, to_id, relation, json, bytes) VALUES (?, ?, ?, ?, ?, ?, ?)').run(session, edge.id, fromId, toId, relation, json, bytes);
          bumpRevision(db, session);
          db.exec('COMMIT');
          return edge;
        } catch (error) { db.exec('ROLLBACK'); throw error; }
      });
    },
    async unlink(sessionId, edgeId) {
      validId(edgeId);
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          const deleted = db.prepare('DELETE FROM relations WHERE session = ? AND id = ?').run(session, edgeId).changes > 0;
          if (deleted) bumpRevision(db, session);
          db.exec('COMMIT');
          return deleted;
        } catch (error) { db.exec('ROLLBACK'); throw error; }
      });
    },
    async graph(sessionId, options = {}) {
      if (!options || typeof options !== 'object' || Array.isArray(options)) throw new Error('invalid_page');
      const { id, cursor = 0, limit = 20, expectedRevision } = options;
      if (id !== undefined) validId(id);
      if (!Number.isSafeInteger(cursor) || cursor < 0 || !Number.isSafeInteger(limit) || limit < 1 || limit > 20) throw new Error('invalid_page');
      validRevision(expectedRevision);
      return run(sessionId, (db, session) => snapshot(db, session, expectedRevision, revision => {
        const focus = id === undefined
          ? db.prepare('SELECT json FROM records WHERE session = ? ORDER BY seq DESC LIMIT 1').get(session)
          : db.prepare('SELECT json FROM records WHERE session = ? AND id = ?').get(session, id);
        if (!focus) return { nodeId: null, nodes: [], edges: [], total: 0, nextCursor: null, revision };
        const record = JSON.parse(focus.json), nodeId = record.id;
        const total = db.prepare('SELECT COUNT(*) AS total FROM relations WHERE session = ? AND (from_id = ? OR to_id = ?)').get(session, nodeId, nodeId).total;
        const edges = db.prepare('SELECT json FROM relations WHERE session = ? AND (from_id = ? OR to_id = ?) ORDER BY seq LIMIT ? OFFSET ?').all(session, nodeId, nodeId, limit, cursor).map(row => JSON.parse(row.json));
        const nodes = new Map([[nodeId, previewNode(record)]]);
        const readNode = db.prepare('SELECT json FROM records WHERE session = ? AND id = ?');
        for (const edge of edges) {
          const neighborId = edge.fromId === nodeId ? edge.toId : edge.fromId;
          if (!nodes.has(neighborId)) nodes.set(neighborId, previewNode(JSON.parse(readNode.get(session, neighborId).json)));
        }
        return { nodeId, nodes: [...nodes.values()], edges, total, nextCursor: cursor + edges.length < total ? cursor + edges.length : null, revision };
      }));
    },
    async graphOverview(sessionId, {page=0, expectedRevision} = {}) {
      if (!Number.isSafeInteger(page) || page < 0 || page > 1_000_000) throw new Error('invalid_page');
      validRevision(expectedRevision);
      return run(sessionId, (db, session) => snapshot(db, session, expectedRevision, revision => {
        const {nodes:totalNodes,edges:totalEdges}=db.prepare(`SELECT (SELECT COUNT(*) FROM records WHERE session = ?) AS nodes,
          (SELECT COUNT(*) FROM relations WHERE session = ?) AS edges`).get(session,session);
        const nodes=db.prepare('SELECT json FROM records WHERE session = ? ORDER BY seq LIMIT 60 OFFSET ?').all(session,page*60)
          .map(({json})=>{const record=JSON.parse(json);return {...previewNode(record),status:record.status??'normal'};});
        const edges=db.prepare('SELECT json FROM relations WHERE session = ? ORDER BY seq LIMIT 120 OFFSET ?').all(session,page*120)
          .map(({json})=>{const {id,fromId,toId,relation}=JSON.parse(json);return {id,fromId,toId,relation};});
        return {nodes,edges,totals:{nodes:totalNodes,edges:totalEdges},revision,nextPage:(page+1)*60<totalNodes||(page+1)*120<totalEdges?page+1:null};
      }));
    },
    async revision(sessionId) { return run(sessionId, (db, session) => readRevision(db, session)); },
    async page(sessionId, cursor = 0, limit = 50, query = '', includeRevision = false) {
      if (!Number.isSafeInteger(cursor) || cursor < 0 || !Number.isSafeInteger(limit) || limit < 1 || limit > 50) throw new Error('invalid_page');
      if (typeof query !== 'string' || Buffer.byteLength(query) > MAX_QUERY_BYTES) throw new Error('invalid_query');
      if (typeof includeRevision !== 'boolean') throw new Error('invalid_revision_flag');
      return run(sessionId, (db, session) => {
        if (!includeRevision) return readPage(db, session, cursor, limit, query);
        // The token and records must belong to the same committed snapshot.
        db.exec('BEGIN');
        try {
          const revision = readRevision(db, session);
          const page = readPage(db, session, cursor, limit, query);
          db.exec('COMMIT');
          return { ...page, revision };
        } catch (error) { db.exec('ROLLBACK'); throw error; }
      });
    },
    async update(sessionId, id, text, expectedText, options) {
      validString(id, 'id', 36);
      if (!RECORD_ID.test(id)) throw new Error('invalid_id');
      validString(text, 'text', MAX_RECORD_BYTES);
      validString(expectedText, 'expected_text', MAX_RECORD_BYTES);
      const aliases = readAliases(options);
      const expectedAliases = aliases === undefined ? undefined : parseAliases(options.expectedAliases, 'expected_aliases', false);
      const sources = readSources(options);
      const expectedSources = sources === undefined ? undefined : readSources(options, 'expectedSources');
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          const row = db.prepare('SELECT json, bytes FROM records WHERE session = ? AND id = ?').get(session, id);
          if (!row) throw new Error('chat_memory_conflict');
          const previous = JSON.parse(row.json);
          if (previous.text !== expectedText) throw new Error('chat_memory_conflict');
          if (expectedAliases !== undefined && !sameAliases(previous.aliases, expectedAliases)) throw new Error('chat_memory_conflict');
          if (expectedSources !== undefined && !sameSources(previous.sources, expectedSources)) throw new Error('chat_memory_conflict');
          const nextAliases = aliases ?? previous.aliases ?? [];
          const nextSources = sources ?? previous.sources ?? [];
          if (previous.text === text && sameAliases(previous.aliases, nextAliases) && sameSources(previous.sources, nextSources)) {
            db.exec('COMMIT');
            return previous;
          }
          const record = { ...previous, text, updatedAt:new Date().toISOString() };
          if (nextAliases.length) record.aliases = nextAliases;
          else delete record.aliases;
          if (nextSources.length) record.sources = nextSources;
          else delete record.sources;
          writeUpdatedRecord(db, session, id, record, row.bytes);
          db.exec('COMMIT');
          return record;
        } catch (err) { db.exec('ROLLBACK'); throw err; }
      });
    },
    async setStatus(sessionId, id, status, expectedStatus) {
      validId(id);
      if (!isChatMemoryStatus(status)) throw new Error('invalid_status');
      if (!isChatMemoryStatus(expectedStatus)) throw new Error('invalid_expected_status');
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          const row = db.prepare('SELECT json, bytes FROM records WHERE session = ? AND id = ?').get(session, id);
          if (!row) throw new Error('chat_memory_conflict');
          const record = JSON.parse(row.json);
          const current = isChatMemoryStatus(record.status) ? record.status : 'normal';
          if (current !== expectedStatus) throw new Error('chat_memory_conflict');
          if (status !== current) {
            if (status === 'normal') delete record.status;
            else record.status = status;
            writeUpdatedRecord(db, session, id, record, row.bytes);
          }
          db.exec('COMMIT');
          return record;
        } catch (err) { db.exec('ROLLBACK'); throw err; }
      });
    },
    async moveFact(sessionId, id, direction, expectedRevision) {
      validId(id);
      if (direction !== 'up' && direction !== 'down') throw new Error('invalid_direction');
      if (expectedRevision === undefined) throw new Error('invalid_revision');
      validRevision(expectedRevision);
      const up = direction === 'up';
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          if (readRevision(db, session) !== expectedRevision) throw new Error('chat_memory_conflict');
          const readSlot = db.prepare('SELECT seq FROM records WHERE session = ? AND id = ?');
          readSlot.setReadBigInts(true);
          const current = readSlot.get(session, id);
          if (!current || current.seq <= 0n) throw new Error('chat_memory_conflict');
          const readAdjacent = db.prepare(`SELECT seq FROM records WHERE session = ? AND seq ${up ? '<' : '>'} ?
            ORDER BY seq ${up ? 'DESC' : 'ASC'} LIMIT 1`);
          readAdjacent.setReadBigInts(true);
          const adjacent = readAdjacent.get(session, current.seq);
          if (adjacent) {
            if (adjacent.seq <= 0n) throw new Error('chat_memory_conflict');
            // Keep the temporary negative slot inside the transaction to avoid the rowid collision.
            const temporary = -current.seq;
            const moveSlot = db.prepare('UPDATE records SET seq = ? WHERE session = ? AND seq = ?');
            for (const [from, to] of [[current.seq, temporary], [adjacent.seq, current.seq], [temporary, adjacent.seq]]) {
              if (moveSlot.run(to, session, from).changes !== 1) throw new Error('chat_memory_conflict');
            }
            bumpRevision(db, session);
          }
          db.exec('COMMIT');
          return !!adjacent;
        } catch (err) { db.exec('ROLLBACK'); throw err; }
      });
    },
    async delete(sessionId, id, {receipt=false} = {}) {
      validString(id, 'id', 36);
      if (!RECORD_ID.test(id)) throw new Error('invalid_id');
      return run(sessionId, (db, session) => {
        db.exec('BEGIN IMMEDIATE');
        try {
          const previous=receipt?db.prepare('SELECT json FROM records WHERE session = ? AND id = ?').get(session,id):null;
          const removedLinks=db.prepare('DELETE FROM relations WHERE session = ? AND (from_id = ? OR to_id = ?)').run(session, id, id).changes;
          const deleted = db.prepare('DELETE FROM records WHERE session = ? AND id = ?').run(session, id).changes > 0;
          const fact=deleted&&previous?JSON.parse(previous.json):null;
          if (deleted) bumpRevision(db, session);
          db.exec('COMMIT');
          return receipt?{deleted,id,...(fact?{fact:{...previewNode(fact),status:fact.status??'normal'},removed_links:removedLinks}:{})}:deleted;
        } catch (error) { db.exec('ROLLBACK'); throw error; }
      });
    },
  };
}
