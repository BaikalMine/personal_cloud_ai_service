import {Buffer} from 'node:buffer';

const MAX_BYTES = 16 * 1024;
const word = source => new RegExp(`(?<![\\p{L}\\p{N}_])(?:${source})(?![\\p{L}\\p{N}_])`, 'iu');
const FILE = String.raw`(?:[A-Za-z]:[\\/]|[\\/])?(?:[\p{L}\p{N}_.-]+[\\/])*[\p{L}\p{N}_-][\p{L}\p{N}_.-]*\.(?:md|markdown|txt|rst|csv|tsv|json|ya?ml|toml|ini|cfg|conf|[cm]?js|tsx?|jsx|s?css|html?|py|go|rs|java|[ch]|cpp|hpp|sh|ps1|sql|xml|docx)`;
const file = new RegExp(`(?<![\\p{L}\\p{N}_.\\/-])${FILE}(?![\\p{L}\\p{N}_\\/-]|\\.[\\p{L}\\p{N}_])`, 'iu');
const files = new RegExp(file.source, 'giu');
const bareFile = new RegExp(`^${FILE}$`, 'iu');
const LINE = String.raw`[1-9]\d{0,6}(?:\s*[-\u2013\u2014]\s*[1-9]\d{0,6})?`;
const lineTarget = word(String.raw`(?:lines?|строк[аи])\s+${LINE}`);
const fileNoun = word('files?|файл(?:е|а|у|ом|ы|ов)?');
const object = word('files?|lines?|blocks?|text|content|paragraphs?|sections?|sentences?|words?|titles?|changes|edits|patch|файл\\p{L}*|строк\\p{L}*|блок\\p{L}*|текст\\p{L}*|абзац\\p{L}*|раздел\\p{L}*|предложени\\p{L}*|слов\\p{L}*|заголов\\p{L}*|правк\\p{L}*|изменени\\p{L}*');

// Requests to describe an edit are not authority; descriptions of its content are valid.
const requestExcluded = word([
  'plan\\w*|explain\\w*|describ\\w*|descriptions?|translat\\w*|draft\\w*|propos\\w*|suggest\\w*|discuss\\w*|summari\\w*|review\\w*|analy[sz]\\w*',
  'план\\p{L}*|объяс\\p{L}*|пояс\\p{L}*|опис\\p{L}*|перев\\p{L}*|предлож\\p{L}*|обсуд\\p{L}*|анализ\\p{L}*',
].join('|'));

// Whole-message vetoes avoid promoting an example's surrounding prose to authority.
const excluded = word([
  'quot\\w*|examples?|samples?',
  'fiction\\w*|stor(?:y|ies)|dialog(?:ue)?|pretend|imagine|hypothetic\\w*|cop(?:y|ied|ying)|characters?|narrat\\w*|said|told|says|saying|spoken|spoke',
  'mean(?:s|ing)?|phrase|what|why|how|whether',
  'repeat|print|say|tell',
  "only|instead|later|tomorrow|wait|pause|stop|cancel|abort|suspend|defer|hold|before|after|unless|until|if|when|once|but|or|maybe|perhaps|probably|possibly|hopefully|hope|think|guess|unsure|uncertain|might|could|would|should|can|cannot|can't|don't|won't|isn't|aren't|not|never|no|without",
  'sorry|apolog\\w*|unable|unavailable|done|complete\\w*|finished|already|applied|edited|updated|replaced|saved|wrote|written|changed|removed|deleted|created|persisted|committed|implemented|have|has|had',
  'цитат\\p{L}*|пример\\p{L}*|образец',
  'рассказ\\p{L}*|истори\\p{L}*|диалог\\p{L}*|персонаж\\p{L}*|вымышл\\p{L}*|представь|скопир\\p{L}*|повтори|напечатай|скажи|сказал\\p{L}*|фраз\\p{L}*|означа\\p{L}*|что|как|зачем|почему',
  'только|вместо|потом|позже|завтра|подожди|пауза|останов\\p{L}*|приостанов\\p{L}*|отмен\\p{L}*|стоп',
  'не|нет|без|ли|но|или|если|когда|пока|после|перед|возможно|наверное|вероятно|надеюсь|думаю|полагаю|может|могу|смогу|мог\\p{L}*|сначала|извин\\p{L}*|простите|прощени\\p{L}*|сожал\\p{L}*|недоступ\\p{L}*',
  'готово|выполн\\p{L}*|заверш\\p{L}*|уже|применил\\p{L}*|внёс\\p{L}*|внесл\\p{L}*|заменил\\p{L}*|изменил\\p{L}*|обновил\\p{L}*|сохранил\\p{L}*|исправил\\p{L}*|удалил\\p{L}*|сделал\\p{L}*',
  '(?:примен[её]н|внес[её]н|замен[её]н|измен[её]н|обновл[её]н|сохран[её]н|исправлен|удал[её]н|сделан)(?:а|о|ы)?',
].join('|'));

const request = /^(?:(?:please|now)\s+)*(?:(?:apply\s+(?:the\s+)?(?:changes|edits|patch))|edit|modify|update|replace|fix|remove|delete|add|write|save)\s+\S|^(?:(?:пожалуйста|сейчас)\s*,?\s+)*(?:(?:примени|давай\s+применим)\s+(?:правки|изменения|патч)|(?:внеси|давай\s+внес[её]м)\s+(?:правки|изменения)|отредактируй|измени|обнови|замени|исправь|удали|добавь|запиши|сохрани)\s+\S/iu;
const future = /^(?:I\s+will|I'll)\s+(?:apply|edit|modify|update|replace|fix|remove|delete|add|write|save)\s+(.+)$|^(?:Я\s+(?:сейчас\s+)?|Сейчас\s+)(?:применю|внесу|отредактирую|изменю|обновлю|заменю|исправлю|удалю|добавлю|запишу|сохраню)\s+(.+)$/iu;
const heading = new RegExp(`^(?:Applying (?:the )?(?:changes|edits) to (?:file )?|Применение правок к файлу )${FILE}[.!]?$`, 'iu');
const target = new RegExp(`^(?:Target|File|Path|Lines?|Цель|Файл|Путь|Строк[аи])\\s*:\\s*(?:${FILE}(?::${LINE})?|(?:(?:lines?|строк[аи])\\s+)?${LINE})[.!]?$`, 'iu');

function prose(value) {
  if (typeof value !== 'string' || value.length > MAX_BYTES || Buffer.byteLength(value, 'utf8') > MAX_BYTES) return null;
  // Backticks are accepted only around a bare file name, never around instructions.
  let quoted = false;
  const text = value.replace(/`([^`\r\n]*)`/g, (_, inner) => {
    if (!bareFile.test(inner)) quoted = true;
    return inner;
  }).replace(/\r\n?/g, '\n').replace(/\u2019/g, "'");
  if (!text.trim() || quoted || /[`~<>\[\]{}"\u00ab\u00bb\u201c\u201d\u201e?\uff1f\u061f;()]/u.test(text)
    || /(^|\n)(?:[ \t]*>|[ \t]{4}|\t|[ \t]*[-\u2013\u2014]\s)/u.test(text)
    || /(?<!\p{L})'|'(?!\p{L})/u.test(text)
    || /[\p{Cc}\p{Cf}]/u.test(text.replace(/[\n\t]/g, ''))
    || excluded.test(text.replace(files, ' '))) return null;
  return text.trim();
}

/**
 * Pure, fail-closed RU/EN prose heuristic, not authorization or tool detection.
 * Oversized/ambiguous input returns false; text is never parsed as a tool call.
 * The caller owns native-tool presence, one-shot recovery and lifecycle guards.
 */
export function detectNarratedEdit(userText, assistantText) {
  const user = prose(userText);
  if (!user || requestExcluded.test(user.replace(files, ' ')) || !request.test(user) || !(file.test(user) || fileNoun.test(user))) return false;
  if (/[.!]\s+\S/u.test(user) || user.split('\n').some(line => line.trim() && !request.test(line.trim()))) return false;
  const assistant = prose(assistantText);
  if (!assistant || !(file.test(assistant) || lineTarget.test(assistant))) return false;
  let intent = false;
  for (const raw of assistant.split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    const match = future.exec(line);
    if (match) {
      const subject = match[1] ?? match[2];
      if (intent || /[.!]\s+\S/u.test(line) || !(object.test(subject) || file.test(subject))) return false;
      intent = true;
    } else if (!heading.test(line) && !target.test(line)) {
      return false;
    }
  }
  return intent;
}
