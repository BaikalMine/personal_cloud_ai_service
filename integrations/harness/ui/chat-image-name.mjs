import { normalizeImageOutputName } from './image-filenames.mjs';

export function chatImageStem(title, id) {
  const shortID = typeof id === 'string' ? Array.from(id.normalize('NFC').replace(/^session-/, '').replace(/[^\p{L}\p{N}_-]+/gu, '')).slice(0, 8).join('') : '';
  const fallback = 'chat-' + (shortID || 'unknown');
  if (typeof title !== 'string') return fallback;
  const safeTitle = title.replace(/[/\\:\p{Cc}\p{Bidi_Control}\u2028\u2029]/gu, '-');
  if (!/[\p{L}\p{N}]/u.test(safeTitle)) return fallback;
  try { return normalizeImageOutputName(safeTitle); }
  catch { return fallback; }
}

export function createChatImageNamer(ctx) {
  let active;
  try {
    ctx?.inject?.(['sessionTitle', 'sessions'], scope => {
      if (typeof scope?.sessionTitle?.get !== 'function' || typeof scope?.sessions?.get !== 'function') {
        active = undefined;
        return;
      }
      const services = {
        get: session => scope.sessionTitle.get(session)?.title,
        lookup: id => scope.sessions.get(id),
      };
      active = services;
      return () => { if (active === services) active = undefined; };
    });
  } catch { active = undefined; }

  return agent => {
    const original = agent?.session;
    const fallback = () => chatImageStem(undefined, original?.id);
    const services = active;
    if (!original || !services) return fallback();
    try {
      let session = original;
      const seen = new Set([session.id]);
      for (let hops = 0; session.header?.origin === 'subagent'; hops++) {
        const parentID = session.header.parentSession;
        if (hops === 8 || typeof parentID !== 'string' || !parentID || seen.has(parentID)) return fallback();
        const parent = services.lookup(parentID);
        if (!parent || parent.id !== parentID) return fallback();
        seen.add(parentID);
        session = parent;
      }
      return chatImageStem(services.get(session), session.id);
    } catch { return fallback(); }
  };
}
