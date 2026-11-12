import test from 'node:test';
import assert from 'node:assert/strict';
import * as classifier from './narrated-edit.mjs';

const {detectNarratedEdit: detect} = classifier;
const ruUser = 'Примени правки к файлу ch0_rev7.md';
const ruAssistant = 'Применение правок к файлу ch0_rev7.md\nЯ заменю финальный блок...\nTarget: Строки 197–201.';
const enUser = 'Please edit the file chapter.md.';
const enAssistant = 'I will replace the final block in chapter.md.';

test('explicit Russian mutation and the reported targeted future narrative', () => {
  for (const user of [ruUser, 'Внеси изменения в файл ch0_rev7.md.', 'Замени финальный блок в файле ch0_rev7.md.']) {
    assert.equal(detect(user, ruAssistant), true, user);
  }
  assert.equal(detect(ruUser, 'Я заменю финальный блок.\nЦель: Строки 197–201.'), true);
  assert.equal(detect('Отредактируй файл.', 'Сейчас исправлю строки 197–201.'), true);
});

test('exact full reported sample describes the edit content, not an explanation request', () => {
  const user = 'Примени правки к файлу ch0_rev7.md.';
  const assistant = 'Применение правок к файлу ch0_rev7.md\nЯ заменю финальный блок текста, начиная с описания комнаты и заканчивая финальным абзацем.\nTarget: Строки 197–201.';
  assert.equal(detect(user, assistant), true);
  assert.equal(detect(user, assistant.replace(/\n/g, '\r\n')), true);
  assert.equal(detect(user, 'Я заменю описание комнаты в файле ch0_rev7.md.'), true);
  assert.equal(detect(user, 'Я заменю финальное предложение в файле ch0_rev7.md.'), true);
  assert.equal(detect(enUser, 'I will replace the room description in chapter.md.'), true);
  assert.equal(detect(enUser, 'I will replace the explanation block in chapter.md.'), true);
  for (const request of ['Примени правки к файлу ch0_rev7.md в описании плана.',
    'Примени правки к файлу ch0_rev7.md в объяснении.', 'Edit chapter.md in a description of the changes.',
    'Edit chapter.md in an explanation of the plan.']) {
    assert.equal(detect(request, assistant), false, request);
  }
  assert.equal(detect(user, 'Я объясню правки к файлу ch0_rev7.md.'), false);
  assert.equal(detect(enUser, 'I will describe the changes to chapter.md.'), false);
});

test('narrow Russian collaborative imperatives explicitly authorize applying edits', () => {
  for (const user of ['Давай применим правки к файлу ch0_rev7.md.',
    'Давай внесем правки в файл ch0_rev7.md.', 'Давай внесём изменения в файл ch0_rev7.md.']) {
    assert.equal(detect(user, ruAssistant), true, user);
  }
  for (const user of ['Давай применим правки к файлу ch0_rev7.md?',
    'Давай внесем правки в файл ch0_rev7.md потом.', 'Давай внесем правки в файл ch0_rev7.md в плане.',
    'Давай не применим правки к файлу ch0_rev7.md.', 'Давай обсудим правки к файлу ch0_rev7.md.',
    'Давай применим правки к файлу ch0_rev7.md в рассказе.', 'Давай применим правки.']) {
    assert.equal(detect(user, ruAssistant), false, user);
  }
});

test('explicit English mutation with a concrete file or line target', () => {
  for (const assistant of [enAssistant, "I'll edit chapter.md.", 'I will apply the changes to `chapter.md`.',
    'Applying changes to chapter.md\nI will replace the final block.\nTarget: lines 197-201.',
    'I will update lines 197-201.', 'I will edit the file.\nFile: chapter.md:197-201',
    'I will edit src/chapter.md.', 'I will edit C:\\drafts\\chapter.md.',
    'I will edit the file.\nTarget: line 197']) {
    assert.equal(detect(enUser, assistant), true, assistant);
  }
  assert.equal(detect('Apply the changes to `chapter.md`.', enAssistant), true);
  assert.equal(detect('Please replace the final block in chapter.md.', enAssistant), true);
});

test('plans, explanations, translation and quoted-example requests do not authorize edits', () => {
  const users = [
    'Plan how to edit chapter.md.', 'Explain how to edit chapter.md.',
    'Translate: Edit chapter.md.', 'Show an example: edit chapter.md.',
    'Copy this example: I will edit chapter.md.', 'Repeat "Edit chapter.md".',
    'Edit chapter.md only in a plan.', 'Edit chapter.md as a hypothetical example.',
    'Edit chapter.md in a fictional story.', 'Edit chapter.md in the dialogue of a character.',
    'Edit chapter.md means what in Russian', 'Edit chapter.md is the phrase to translate.',
    'Составь план правок файла ch0_rev7.md.', 'Объясни, как изменить файл ch0_rev7.md.',
    'Переведи: Примени правки к файлу ch0_rev7.md.', 'Скопируй пример: Примени правки к файлу ch0_rev7.md.',
    'Примени правки к файлу ch0_rev7.md только на словах.',
    'Примени правки к файлу ch0_rev7.md в рассказе персонажа.',
    '"Edit chapter.md"', '«Примени правки к файлу ch0_rev7.md»',
    '> Edit chapter.md', '```text\nEdit chapter.md\n```', '    Edit chapter.md',
  ];
  for (const user of users) assert.equal(detect(user, enAssistant), false, user);
});

test('quotes and whole examples cannot donate future intent or targets', () => {
  const assistants = [
    '```text\nI will edit chapter.md.\n```', '~~~text\nI will edit chapter.md.\n~~~',
    '> I will edit chapter.md.', '    I will edit chapter.md.', '\tI will edit chapter.md.',
    '`I will edit chapter.md.`', '"I will edit chapter.md."', "'I will edit chapter.md.'",
    '“I will edit chapter.md.”', '«Я заменю финальный блок в файле ch0_rev7.md.»',
    'Example:\nI will edit chapter.md.', 'Sample response:\nI will edit chapter.md.',
    'Here is the quoted example.\nI will edit chapter.md.',
    'Alice said:\nI will edit chapter.md.', 'I will edit chapter.md. She smiled.',
    'I will edit chapter.md in the story.', 'I will edit chapter.md as an example.',
    '— Я заменю финальный блок в файле ch0_rev7.md.',
    'Пример ответа:\nЯ заменю финальный блок в файле ch0_rev7.md.',
    'Я заменю финальный блок в файле ch0_rev7.md, сказал персонаж.',
    'I will replace the final block.\n> Target: chapter.md',
    'I will replace the final block.\n```\nTarget: chapter.md\n```',
    'I will replace the final block.\nTarget: `lines 197-201`',
  ];
  for (const assistant of assistants) assert.equal(detect(enUser, assistant), false, assistant);
  assert.equal(detect('Edit chapter.md.\n> Example only', enAssistant), false);
  assert.equal(detect('Edit chapter.md.\n```\nexample\n```', enAssistant), false);
});

test('completion claims, including mixed future/completed reports, are rejected', () => {
  const assistants = [
    'I edited chapter.md.', 'Done. I replaced the final block in chapter.md.',
    'The file chapter.md has been updated.', 'I will edit chapter.md.\nThe changes are applied.',
    'I will edit chapter.md and have saved the changes.', 'I will edit chapter.md, already changed.',
    'I will edit chapter.md, changes committed.', 'I will edit chapter.md, changes written.',
    'Я заменил финальный блок в файле ch0_rev7.md.', 'Правки в файле ch0_rev7.md применены.',
    'Я заменю финальный блок в файле ch0_rev7.md.\nГотово.',
    'Я заменю финальный блок в файле ch0_rev7.md, изменения уже внесены.',
    'Я заменю финальный блок в файле ch0_rev7.md, файл обновлён.',
    'Я заменю финальный блок в файле ch0_rev7.md, правки применены.',
    'Я заменю финальный блок в файле ch0_rev7.md, исправления сделаны.',
  ];
  for (const assistant of assistants) assert.equal(detect(enUser, assistant), false, assistant);
});

test('questions, apologies, unavailability, conditionals and uncertainty fail closed', () => {
  const assistants = [
    'I will edit chapter.md. Shall I proceed?', 'I will edit chapter.md?',
    'Can I edit chapter.md', 'I can edit chapter.md.', 'I would edit chapter.md.',
    'I will edit chapter.md if you approve.', 'I will edit chapter.md after approval.',
    'I will edit chapter.md, perhaps.', 'Sorry, I will edit chapter.md.',
    'I will edit chapter.md, probably.', 'I will edit chapter.md, I hope.',
    "I will edit chapter.md, the tool isn't available.",
    'I will edit chapter.md.\nTools are unavailable.', 'I will not edit chapter.md.',
    "I won't edit chapter.md.", 'I will edit chapter.md when tools return.',
    'Я заменю финальный блок в файле ch0_rev7.md?',
    'Я заменю финальный блок в файле ch0_rev7.md, если разрешите.',
    'Возможно, я заменю финальный блок в файле ch0_rev7.md.',
    'Я заменю финальный блок в файле ch0_rev7.md.\nИзвините, инструмент недоступен.',
    'Я не заменю финальный блок в файле ch0_rev7.md.',
    'Я заменю финальный блок в файле ch0_rev7.md, наверное.',
    'Я заменю финальный блок в файле ch0_rev7.md, надеюсь.',
    'Я заменю финальный блок в файле ch0_rev7.md, простите.',
  ];
  for (const assistant of assistants) assert.equal(detect(enUser, assistant), false, assistant);
  for (const user of ['Can you edit chapter.md?', 'Edit chapter.md?', 'Please edit chapter.md if possible.',
    "Edit chapter.md but don't save.", 'Можешь изменить файл ch0_rev7.md?', 'Не изменяй файл ch0_rev7.md.']) {
    assert.equal(detect(user, enAssistant), false, user);
  }
});

test('pause/cancel commands do not authorize recovery, even alongside an edit request', () => {
  for (const user of ['Stop', 'Pause', 'Cancel', 'Wait', 'Edit chapter.md later.',
    'Edit chapter.md.\nStop.', 'Edit chapter.md, wait for me.', 'Edit chapter.md tomorrow.',
    'Стоп', 'Пауза', 'Отмени', 'Подожди', 'Примени правки к файлу ch0_rev7.md потом.',
    'Примени правки к файлу ch0_rev7.md.\nОстановись.']) {
    assert.equal(detect(user, ruAssistant), false, user);
  }
  assert.equal(detect('Edit chapter.md, abort.', enAssistant), false);
  assert.equal(detect('Примени правки к файлу ch0_rev7.md, приостанови.', ruAssistant), false);
});

test('future intent must be a direct mutation, not any I will statement', () => {
  for (const assistant of ['I will look at chapter.md.', 'I will read chapter.md.',
    'I will think about editing chapter.md.', 'I will try to edit chapter.md.',
    'I will consider replacing the block in chapter.md.', 'I will discuss chapter.md.',
    'I will replace my laptop.\nTarget: lines 197-201', 'The assistant will edit chapter.md.',
    'Я прочитаю файл ch0_rev7.md.', 'Я попробую заменить блок в файле ch0_rev7.md.',
    'Я подумаю о правках файла ch0_rev7.md.', 'Замени блок в файле ch0_rev7.md.',
    'I will apply changes.', 'I will edit the file.', 'I will replace the final block.\nTarget: final section',
    'I will edit line 0.', 'I will edit chapter.unknownextension.', 'Target: chapter.md']) {
    assert.equal(detect(enUser, assistant), false, assistant);
  }
});

test('printed JSON/XML/tool/code text is inert, including prose mixed with calls', () => {
  const payloads = [
    '{"tool_calls":[{"type":"function","function":{"name":"edit_precise","arguments":"I will edit chapter.md."}}]}',
    '{"name":"edit","arguments":{"path":"chapter.md","text":"I will edit chapter.md."}}',
    '<tool_call name="edit">I will edit chapter.md.</tool_call>',
    '```js\nglobalThis.__narratedEditExecuted = true;\n```',
    'globalThis.__narratedEditExecuted = true; // I will edit chapter.md.',
    'I will edit chapter.md.\n{"tool":"edit","path":"chapter.md"}',
    'I will edit chapter.md.\n<tool_call>edit</tool_call>',
  ];
  for (const payload of payloads) {
    assert.equal(detect(enUser, payload), false, payload);
    assert.equal(detect(payload, enAssistant), false, payload);
  }
  assert.equal(Object.hasOwn(globalThis, '__narratedEditExecuted'), false);
});

test('normal chat, non-Russian/English requests and implicit authority are negative', () => {
  for (const user of ['Hello', 'Thanks', 'chapter.md', 'Go ahead', 'Yes, do it', 'Looks good',
    'I will edit chapter.md.', 'We should edit chapter.md.', 'Fix this', 'Please apply these changes.',
    'Привет', 'Да, сделай', 'Исправь это', 'Примени эти изменения.',
    'Modifica el archivo chapter.md.', 'Bitte bearbeite chapter.md.', '修改文件 chapter.md']) {
    assert.equal(detect(user, enAssistant), false, user);
  }
  for (const assistant of ['Hello', 'Gracias', 'Editaré chapter.md.', 'Ich werde chapter.md bearbeiten.',
    '我会修改文件 chapter.md', 'Everything looks good in chapter.md.']) {
    assert.equal(detect(enUser, assistant), false, assistant);
  }
});

test('each input has a strict 16 KiB UTF-8 bound; overflow is not truncated', () => {
  const limit = 16 * 1024;
  const paddedUser = enUser + ' '.repeat(limit - Buffer.byteLength(enUser));
  const paddedAssistant = enAssistant + ' '.repeat(limit - Buffer.byteLength(enAssistant));
  assert.equal(detect(paddedUser, paddedAssistant), true);
  assert.equal(detect(paddedUser + ' ', enAssistant), false);
  assert.equal(detect(enUser, paddedAssistant + ' '), false);
  assert.equal(detect(paddedUser + ' stop', enAssistant), false);
  assert.equal(detect(enUser, paddedAssistant + ' Done.'), false);
  const ruPadding = ruUser + 'я'.repeat(Math.floor((limit - Buffer.byteLength(ruUser)) / 2) + 1);
  assert.ok(ruPadding.length < limit);
  assert.equal(detect(ruPadding, ruAssistant), false);
  const wideAssistant = ruAssistant + 'я'.repeat(Math.floor((limit - Buffer.byteLength(ruAssistant)) / 2) + 1);
  assert.ok(wideAssistant.length < limit);
  assert.equal(detect(ruUser, wideAssistant), false);
  assert.equal(detect('x'.repeat(2_000_000), enAssistant), false);
  assert.equal(detect(enUser, 'x'.repeat(2_000_000)), false);
});

test('strict primitive input and boolean-only export contract, without coercion', () => {
  assert.deepEqual(Object.keys(classifier), ['detectNarratedEdit']);
  const poison = {toString() { throw new Error('must not coerce'); }};
  for (const value of [undefined, null, false, 1, {}, [], poison, new String(enUser), '', '   ']) {
    assert.equal(detect(value, enAssistant), false);
    assert.equal(detect(enUser, value), false);
  }
  for (let i = 0; i < 3; i++) {
    assert.equal(detect(ruUser, ruAssistant), true);
    assert.equal(detect(enUser, 'normal chat'), false);
  }
  assert.equal(detect(enUser, 'I will edit chapter.md.\u0000'), false);
  assert.equal(detect(enUser, 'I will edit chapter.md.\u202e'), false);
});
