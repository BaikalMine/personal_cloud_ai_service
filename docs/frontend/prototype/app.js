"use strict";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const escapeHTML = (value) =>
  String(value).replace(
    /[&<>"']/g,
    (char) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        char
      ],
  );
const icon = (name, cls = "") =>
  `<i data-lucide="${name}" class="${cls}" aria-hidden="true"></i>`;
const icons = () =>
  window.lucide.createIcons({
    attrs: { "aria-hidden": "true", focusable: "false" },
  });
const read = (key, fallback) => {
  try {
    return JSON.parse(localStorage.getItem(key)) ?? fallback;
  } catch (_) {
    return fallback;
  }
};
const write = (key, value) => {
  try {
    localStorage.setItem(key, JSON.stringify(value));
    return true;
  } catch (_) {
    return false;
  }
};
const photos = [
  {
    id: "portrait",
    name: "Портрет с цветным светом",
    file: "portrait.jpg",
    model: "Krea2 Turbo",
    size: "1024 × 1280",
    alt: "Женщина с тёмными волосами в чёрном топе, холодный боковой свет",
    type: "image",
    favorite: true,
  },
  {
    id: "landscape",
    name: "Дорога среди скал",
    file: "landscape.jpg",
    model: "Flux2",
    size: "1024 × 1536",
    alt: "Прямая дорога среди красных скал под светлым небом",
    type: "image",
    favorite: false,
  },
  {
    id: "portrait-2",
    name: "Студийный портрет",
    file: "portrait-2.jpg",
    model: "Krea2 Raw",
    size: "1024 × 1536",
    alt: "Женщина со светло-русыми волосами у тёмного фона",
    type: "image",
    favorite: false,
  },
  {
    id: "interior",
    name: "Свет и геометрия",
    file: "interior.jpg",
    model: "Flux2",
    size: "1536 × 1024",
    alt: "Чёрный торшер возле светло-зелёной стены",
    type: "image",
    favorite: false,
  },
];
const state = {
  page: "studio",
  kind: "image",
  editModel: "Flux2",
  mode: "exact",
  mobile: "settings",
  activePhoto: 0,
  refs: [null, null, null, null],
  filter: "all",
  search: "",
  trainingTab: "dataset",
  captionIndex: 0,
  userSearch: "",
  userFilter: "all",
  cancelled: false,
  selectedUser: 0,
  pickerSlot: 0,
  prompt: read(
    "nd-design-prompt",
    "Студийный портрет женщины в чёрном топе. Холодный боковой свет, спокойный прямой взгляд, естественная фактура кожи. Чистый фон, крупный план.",
  ),
  captions: read("nd-design-captions", [
    "nd_light, close-up portrait of a woman with dark slicked-back hair, a black crew-neck top, blue side lighting, a pale lavender background, direct gaze, visible natural skin texture.",
    "nd_light, studio portrait of a woman with long light-brown hair, a black sheer-sleeved top, one hand near her hair, dark grey background, soft frontal lighting.",
  ]),
  users: [
    {
      name: "Алексей",
      login: "alexey",
      type: "Постоянный",
      role: "Администратор",
      photos: 120,
      videos: 30,
      quality: "1440",
      priority: "Высокий",
      expires: "Без удаления",
      train: true,
      active: true,
    },
    {
      name: "Анна",
      login: "anna.studio",
      type: "Постоянный",
      role: "Пользователь",
      photos: 80,
      videos: 10,
      quality: "1080",
      priority: "Обычный",
      expires: "Без удаления",
      train: true,
      active: true,
    },
    {
      name: "Михаил",
      login: "mikhail",
      type: "Временный",
      role: "Пользователь",
      photos: 20,
      videos: 3,
      quality: "720",
      priority: "Обычный",
      expires: "Через 2 д. 14 ч.",
      train: false,
      active: true,
    },
    {
      name: "Елена",
      login: "elena.design",
      type: "Временный",
      role: "Пользователь",
      photos: 12,
      videos: 2,
      quality: "480",
      priority: "Обычный",
      expires: "Через 5 ч. 42 мин.",
      train: false,
      active: true,
    },
    {
      name: "Дмитрий",
      login: "dmitry",
      type: "Постоянный",
      role: "Пользователь",
      photos: 50,
      videos: 5,
      quality: "720",
      priority: "Обычный",
      expires: "Без удаления",
      train: false,
      active: false,
    },
  ],
};
const main = $("#main");
const dialog = $("#dialog");
let toastTimer;
let focusBeforeDialog;
let uploadSlot = 0;

function toast(message) {
  $("#toast").textContent = message;
  $("#toast").classList.add("visible");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => $("#toast").classList.remove("visible"), 4200);
}

function button(action, text, symbol, cls = "", attrs = "") {
  return `<button type="button" data-action="${action}" class="${cls}" ${attrs}>${symbol ? icon(symbol) : ""}${text}</button>`;
}

function iconButton(action, label, symbol, attrs = "") {
  return button(
    action,
    "",
    symbol,
    "icon-button",
    `aria-label="${label}" title="${label}" ${attrs}`,
  );
}

function options(values, selected) {
  return values
    .map((item) => {
      const [value, label] = Array.isArray(item) ? item : [item, item];
      return `<option value="${escapeHTML(value)}" ${String(value) === String(selected) ? "selected" : ""}>${escapeHTML(label)}</option>`;
    })
    .join("");
}

function selectField(id, label, values, selected, hint = "") {
  return `<div class="field"><label for="${id}">${label}</label><select id="${id}">${options(values, selected)}</select>${hint ? `<small class="field-hint">${hint}</small>` : ""}</div>`;
}

function inputField(id, label, value, type = "number", extra = "") {
  return `<div class="field"><label for="${id}">${label}</label><input id="${id}" type="${type}" value="${escapeHTML(value)}" ${extra}></div>`;
}

function heading(title, subtitle, actions = "") {
  return `<div class="page-heading"><div><h1>${title}</h1>${subtitle ? `<p>${subtitle}</p>` : ""}</div>${actions}</div>`;
}

function details(title, content, count = "") {
  return `<details class="details"><summary>${title}<span class="count">${count}</span>${icon("chevron-down", "chevron")}</summary><div class="details-body">${content}</div></details>`;
}

function check(id, title, subtitle = "", checked = false, data = "") {
  return `<div class="check-row"><label for="${id}"><span>${title}${subtitle ? `<small>${subtitle}</small>` : ""}</span></label><input class="switch" id="${id}" type="checkbox" ${checked ? "checked" : ""} ${data}></div>`;
}

function showDialog(title, content, footer = "", wide = false) {
  if (!dialog.open) focusBeforeDialog = document.activeElement;
  dialog.classList.toggle("wide", wide);
  $("#dialog-content").innerHTML =
    `<div class="dialog-header"><h2 id="dialog-title">${title}</h2>${iconButton("close", "Закрыть", "x")}</div><div class="dialog-body">${content}</div>${footer ? `<div class="dialog-footer">${footer}</div>` : ""}`;
  if (!dialog.open) dialog.showModal();
  icons();
}

function closeDialog() {
  dialog.close();
}

dialog.addEventListener("close", () => {
  if (focusBeforeDialog?.isConnected) focusBeforeDialog.focus();
});

function photoMarkup(photo, cls = "", lazy = true) {
  return `<img class="${cls}" src="assets/${photo.file}" alt="${photo.alt}" ${lazy ? 'loading="lazy"' : ""}>`;
}

function referenceSlots() {
  const count =
    state.kind === "edit"
      ? state.editModel === "Krea2 Edit"
        ? 2
        : 4
      : state.mode === "free"
        ? 4
        : 2;
  const slots = Array.from({ length: count }, (_, i) => {
    const ref = state.refs[i];
    const title =
      state.kind === "video" && state.mode === "exact"
        ? i === 0
          ? "Первый кадр"
          : "Последний кадр"
        : `Референс ${i + 1}`;
    return `<div class="ref-slot"><div class="ref-slot-title">${title}<span>Необяз.</span></div><div class="ref-preview">${ref ? `<img src="${escapeHTML(ref.src)}" alt="${escapeHTML(ref.alt)}">${iconButton("remove-ref", "Убрать референс " + (i + 1), "x", `data-index="${i}"`)}` : icon("image-plus")}</div><div class="ref-sources">${button("upload", "Устройство", "upload", "", `data-slot="${i}"`)}${button("picker", "Результаты", "images", "", `data-slot="${i}"`)}</div></div>`;
  }).join("");
  return `<div class="ref-grid">${slots}</div>${state.kind === "video" && state.mode === "free" ? `<div class="row wrap">${button("audio", "Аудио", "audio-lines", "ghost")}${button("video-ref", "Видео", "film", "ghost")}</div>` : ""}`;
}

function processing() {
  const video = state.kind === "video";
  const entries = video
    ? [
        [
          "rife",
          "Плавность движения",
          "RIFE",
          selectField(
            "rife-rate",
            "Множитель кадров",
            [["2", "2× · 48 FPS"]],
            "2",
          ),
        ],
        [
          "rtx",
          "Увеличить разрешение",
          "RTX",
          selectField(
            "rtx-scale",
            "Масштаб",
            [
              ["1.5", "1,5×"],
              ["2", "2×"],
            ],
            "2",
          ),
        ],
        [
          "sharpen",
          "Резкость",
          "",
          inputField(
            "sharp-strength",
            "Сила",
            "0.8",
            "number",
            'min="0" max="2" step="0.1"',
          ),
        ],
      ]
    : [
        [
          "refine",
          "Доработка деталей",
          "Refinement",
          inputField("refine-steps", "Шаги", "2", "number", 'min="1" max="20"'),
        ],
        [
          "upscale",
          "Увеличить разрешение",
          "",
          selectField(
            "upscale-scale",
            "Масштаб",
            [
              ["1.5", "1,5×"],
              ["2", "2×"],
            ],
            "2",
          ),
        ],
        [
          "color",
          "Сохранить палитру",
          "Color correction",
          inputField(
            "color-strength",
            "Сила",
            "1",
            "number",
            'min="0" max="1" step="0.1"',
          ),
        ],
      ];
  return entries
    .map(
      ([id, title, sub, fields]) =>
        `<div class="processing-unit">${check(id, title, sub, false, "data-processing")}<div class="processing-options" data-options="${id}" hidden>${fields}</div></div>`,
    )
    .join("");
}

function renderStudio() {
  const video = state.kind === "video";
  const hasRefs = state.kind !== "image";
  const loraChoices = video
    ? [["motion", "Motion · MiniMax H3"]]
    : state.kind === "edit" && state.editModel === "Flux2"
      ? [["film", "Film Texture · Flux2"]]
      : [
          ["light", "Studio Light · Krea2"],
          ["film", "Film Texture · Krea2"],
        ];
  main.innerHTML =
    heading(
      "Создать",
      "",
      `<span class="meta">${icon("check")} Черновик на этом устройстве</span>`,
    ) +
    `
    <div class="tabs" role="tablist" aria-label="Тип контента">
      ${[
        ["image", "Изображение", "image"],
        ["edit", "Редактирование", "sliders-horizontal"],
        ["video", "Видео", "film"],
      ]
        .map(
          ([kind, label, sym]) =>
            `<button role="tab" aria-selected="${state.kind === kind}" data-action="kind" data-kind="${kind}">${icon(sym)}${label}</button>`,
        )
        .join("")}
    </div>
    <div class="segmented mobile-result-tabs" role="group" aria-label="Рабочая область"><button data-action="mobile-view" data-view="settings" aria-pressed="${state.mobile === "settings"}">Настройки</button><button data-action="mobile-view" data-view="result" aria-pressed="${state.mobile === "result"}">Результат</button></div>
    <div class="studio" data-mobile="${state.mobile}">
      <form class="form-column" id="generation-form">
        <div class="form-content">
          ${video ? `<div class="stack"><div class="field-row-title">Сценарий${iconButton("video-guide", "О режимах видео", "circle-help")}</div><div class="radio-row"><label class="radio-choice"><input type="radio" name="video-mode" value="exact" ${state.mode === "exact" ? "checked" : ""}><span><strong>По описанию и кадрам</strong><small>Текст, первый и последний кадр</small></span></label><label class="radio-choice"><input type="radio" name="video-mode" value="free" ${state.mode === "free" ? "checked" : ""}><span><strong>По референсам</strong><small>До 4 фото, аудио и видео</small></span></label></div></div>` : ""}
          ${selectField("model", "Модель", video ? ["MiniMax H3 · v5"] : state.kind === "edit" ? ["Flux2", "Krea2 Edit"] : ["Krea2 Turbo", "Krea2 Raw", "Krea2 · Gonzo v4"], video ? "MiniMax H3 · v5" : state.kind === "edit" ? state.editModel : "Krea2 Turbo")}
          ${hasRefs ? `<div class="stack"><div class="field-row-title">${video && state.mode === "exact" ? "Кадры" : "Референсы"}</div><div id="reference-slots">${referenceSlots()}</div></div>` : ""}
          <div class="prompt-field"><div class="field-row-title"><label for="prompt">Промпт</label>${button("assistant", "Помочь с промптом", "sparkles", "ghost")}</div><textarea id="prompt" spellcheck="false" required>${escapeHTML(state.prompt)}</textarea><div class="prompt-footer"><span id="prompt-length">${state.prompt.length} символов</span><span class="saved-indicator" id="draft-save">${icon("check")} Сохранено локально</span></div></div>
          ${
            video
              ? `<div class="fields-2">${selectField(
                  "duration",
                  "Длительность",
                  [
                    ["5", "5 секунд"],
                    ["10", "10 секунд"],
                    ["15", "15 секунд"],
                  ],
                  "10",
                )}${selectField(
                  "quality",
                  "Базовое качество",
                  [
                    ["480", "480p"],
                    ["720", "720p"],
                    ["1080", "1080p"],
                    ["1440", "1440p · 2K"],
                  ],
                  "480",
                )}</div><div class="hint-inline">${icon("ratio")}<span>Пропорции из первого фото. Без фото: 16:9.</span></div>`
              : `<div class="fields-2">${selectField("aspect", "Пропорции", ["4:5", "3:4", "1:1", "16:9", "9:16"], "4:5")}${selectField(
                  "batch",
                  "Количество",
                  [
                    ["1", "1 изображение"],
                    ["4", "4 изображения"],
                    ["8", "8 изображений"],
                  ],
                  "4",
                )}</div>`
          }
        </div>
        ${details("LoRA", selectField("lora-model", "Добавить LoRA", [["none", "Не выбрана"], ...loraChoices], "none") + `<div id="lora-weight" hidden>${inputField("lora-strength", "Сила LoRA", "0.7", "number", 'min="0" max="2" step="0.05"')}</div>`, "Не выбраны")}
        ${details("Обработка", processing(), "Выключена")}
        ${details("Точные настройки", `<div class="fields-2">${inputField("seed", "Seed", "-1", "number", 'min="-1"')}${inputField("steps", "Шаги", video ? "25" : "8", "number", 'min="1" max="100"')}</div><div class="fields-2">${selectField("sampler", "Сэмплер", ["Euler", "DPM++ 2M"], "Euler")}${selectField("scheduler", "Планировщик", ["Simple", "Beta"], "Simple")}</div>${check("sage", "SageAttention", "Опционально")}${check("vram", "Освобождать видеопамять", "", true)}`)}
        <div class="submit-bar"><div class="submit-meta"><span id="submit-summary">${video ? "10 сек. · 480p" : "4 изображения · 4:5"}</span><span>Обычный приоритет</span></div><button class="primary" type="submit">${icon(video ? "film" : "sparkles")}${video ? "Создать видео" : "Создать изображение"}</button></div>
      </form>
      <section class="result-column" aria-label="Последний результат">
        <div class="result-header"><div class="row"><h2>Последний результат</h2><span class="status good">${icon("circle-check")}Готово</span></div>${iconButton("result-info", "Параметры результата", "info")}</div>
        <div class="image-stage">${photoMarkup(photos[state.activePhoto], "", false)}<span class="stage-label">${photos[state.activePhoto].size}</span></div>
        <div class="result-actions"><div class="row">${button("animate", "В видео", "film", "primary")}${iconButton("edit-result", "Редактировать изображение", "sliders-horizontal")}${iconButton("download", "Скачать изображение", "download")}${iconButton("favorite", "В избранное", "star")}</div>${button("repeat", "Повторить", "rotate-ccw", "ghost")}</div>
        <div class="row spread result-meta"><span>${photos[state.activePhoto].model} · 1 мин. 42 сек.</span><span>Сегодня, 14:32</span></div>
        <div class="thumb-strip">${photos.map((photo, i) => `<button aria-label="Результат: ${photo.name}" aria-pressed="${i === state.activePhoto}" data-action="select-result" data-index="${i}">${photoMarkup(photo)}</button>`).join("")}${button("library", "", "images", "thumb-new", 'aria-label="Все результаты" title="Все результаты"')}</div>
        <div class="result-note">${icon("clock-3")}Удалится через 22 ч. ${button("pin", "Закрепить на 30 дней", "", "ghost")}</div>
      </section>
    </div>`;
}

function mediaTile(photo, index, picker = false) {
  return `<article class="media-item"><button class="media-open" data-action="${picker ? "pick-photo" : "view-photo"}" data-index="${index}" aria-label="${picker ? "Выбрать" : "Открыть"}: ${photo.name}">${photoMarkup(photo)}<span class="media-mark">${icon("image")}${photo.model}</span></button><div class="media-caption"><div><strong>${photo.name}</strong><small>${photo.size} · ${photo.favorite ? "В избранном" : "Сегодня, 14:32"}</small></div>${picker ? "" : iconButton("media-menu", "Действия: " + photo.name, "ellipsis", `data-index="${index}"`)}</div></article>`;
}

function libraryItems() {
  return photos
    .map((photo, index) => ({ photo, index }))
    .filter(
      ({ photo }) =>
        (state.filter !== "favorites" || photo.favorite) &&
        state.filter !== "video" &&
        photo.name
          .toLocaleLowerCase()
          .includes(state.search.toLocaleLowerCase()),
    );
}

function renderLibrary() {
  main.innerHTML =
    heading(
      "Результаты",
      "4 изображения",
      button("new", "Создать", "plus", "primary"),
    ) +
    `
    <div class="toolbar"><div class="filters" role="group" aria-label="Фильтр результатов">${[
      ["all", "Все"],
      ["image", "Изображения"],
      ["video", "Видео"],
      ["favorites", "Избранное"],
    ]
      .map(([f, label]) =>
        button(
          "filter",
          label,
          "",
          "",
          `data-filter="${f}" aria-pressed="${f === state.filter}"`,
        ),
      )
      .join(
        "",
      )}</div><div class="search">${icon("search")}<input type="search" id="library-search" aria-label="Поиск результатов" placeholder="Поиск по названию" value="${escapeHTML(state.search)}"></div></div>
    <div class="info-band">${icon("clock-3")}<span>Обычные результаты хранятся 24 часа. Закреплённые: 30 дней.</span>${button("storage-info", "Хранение", "circle-help", "ghost")}</div>
    <div id="library-items"></div>`;
  updateLibrary();
}

function updateLibrary() {
  const items = libraryItems();
  $("#library-items").innerHTML = items.length
    ? `<div class="date-line">Сегодня · ${items.length}</div><div class="library-grid">${items.map(({ photo, index }) => mediaTile(photo, index)).join("")}</div>`
    : `<div class="empty">${icon("images")}<h2>Пока ничего нет</h2><p>${state.search ? "Нет совпадений. Попробуйте другое название." : "В этом разделе ещё нет результатов."}</p>${button("reset-filter", "Показать все", "arrow-left")}</div>`;
  icons();
}

function renderTraining() {
  const selected = state.captionIndex === 0 ? photos[0] : photos[2];
  main.innerHTML =
    heading(
      "Мои LoRA",
      "Модели и материалы для обучения",
      button(
        "training-help",
        "",
        "circle-help",
        "icon-button",
        'aria-label="Об обучении LoRA" title="Об обучении LoRA"',
      ),
    ) +
    `
    <div class="tabs" role="tablist" aria-label="Раздел обучения"><button role="tab" aria-selected="${state.trainingTab === "models"}" data-action="training-tab" data-tab="models">Обученные LoRA <span class="tag">2</span></button><button role="tab" aria-selected="${state.trainingTab === "dataset"}" data-action="training-tab" data-tab="dataset">Датасеты <span class="tag">1</span></button></div>
    ${
      state.trainingTab === "models"
        ? `<div class="lora-list">${[
            ["Студийный свет", photos[0], "Krea2 Raw · 1200 шагов"],
            ["Плёночная фактура", photos[1], "Flux2 Klein · 900 шагов"],
          ]
            .map(
              ([name, photo, meta]) =>
                `<div class="lora-row">${photoMarkup(photo)}<div class="grow"><h3>${name}</h3><small>${meta}</small></div><span class="status good">${icon("circle-check")}Готова</span>${button("use-lora", "Использовать", "plus")}${iconButton("delete-lora", "Удалить LoRA " + name, "trash-2")}</div>`,
            )
            .join("")}</div>`
        : `
    <div class="dataset-heading"><div><h2>Студийный свет</h2><p>Стиль · 2 изображения · Черновик</p></div>${button("dataset-menu", '<span class="button-text">Действия</span>', "ellipsis")}</div>
    <div class="step-track" aria-label="Этапы обучения"><span><b>1</b>Материалы</span><span class="current" aria-current="step"><b>2</b>Описания</span><span><b>3</b>Обучение</span><span><b>4</b>Результат</span></div>
    <div class="dataset-workspace"><aside class="dataset-rail"><div class="row spread"><h3>Кадры</h3>${iconButton("dataset-upload", "Добавить фото в датасет", "plus")}</div><div class="dataset-thumbs">${[photos[0], photos[2]].map((photo, i) => `<button data-action="caption-photo" data-index="${i}" aria-label="Кадр ${i + 1}" aria-pressed="${i === state.captionIndex}">${photoMarkup(photo)}<span>0${i + 1}</span></button>`).join("")}</div></aside>
    <div class="dataset-photo">${photoMarkup(selected, "", false)}</div>
    <section class="caption-editor"><div class="row spread"><h3>Кадр 0${state.captionIndex + 1}</h3><span class="saved-indicator" id="caption-save">${icon("check")}Сохранено локально</span></div>${inputField("trigger", "Триггер-слово", "nd_light", "text", "readonly")}<div class="field"><label for="caption">Описание кадра</label><textarea id="caption" spellcheck="false">${escapeHTML(state.captions[state.captionIndex])}</textarea></div>${button("caption-assistant", "Предложить описание", "sparkles")}<div class="hint-inline">${icon("scan-eye")}<span>Описывается только выбранный кадр.</span></div><div class="row spread">${button("caption-prev", "Назад", "chevron-left", "ghost", state.captionIndex === 0 ? "disabled" : "")}${button("caption-next", "Далее", "chevron-right", "ghost", state.captionIndex === 1 ? "disabled" : "")}</div></section></div>
    <div class="dataset-footer"><span class="status good">${icon("circle-check")}У обоих кадров есть описания</span>${button("training-setup", "К настройкам обучения", "arrow-right", "primary")}</div>`
    }`;
}

function renderUsers() {
  main.innerHTML =
    heading(
      "Пользователи",
      "Права, лимиты и срок доступа",
      button("invite", "Пригласить", "user-plus", "primary"),
    ) +
    `
    <div class="toolbar"><div class="search">${icon("search")}<input type="search" id="user-search" aria-label="Поиск пользователей" placeholder="Имя или логин" value="${escapeHTML(state.userSearch)}"></div><div class="filters" role="group" aria-label="Тип пользователя">${[
      ["all", "Все"],
      ["permanent", "Постоянные"],
      ["temporary", "Временные"],
    ]
      .map(([f, text]) =>
        button(
          "user-filter",
          text,
          "",
          "",
          `data-filter="${f}" aria-pressed="${state.userFilter === f}"`,
        ),
      )
      .join("")}</div></div><div id="user-table"></div>`;
  updateUsers();
}

function updateUsers() {
  const users = state.users
    .map((user, index) => ({ user, index }))
    .filter(
      ({ user }) =>
        (state.userFilter === "all" ||
          (state.userFilter === "temporary") === (user.type === "Временный")) &&
        (user.name + " " + user.login)
          .toLowerCase()
          .includes(state.userSearch.toLowerCase()),
    );
  $("#user-table").innerHTML = users.length
    ? `<div class="table-wrap"><table><thead><tr><th>Пользователь</th><th class="user-col-type">Учётная запись</th><th>Статус</th><th class="user-col-quota">Остаток лимита</th><th class="user-col-priority">Приоритет</th><th class="user-col-expiry">Удаление</th><th><span class="small">Права</span></th></tr></thead><tbody>${users.map(({ user: u, index }) => `<tr><td><div class="identity"><span class="avatar">${u.name.slice(0, 1)}</span><div><span class="user-name">${u.name}</span><small>${u.login}</small></div></div><div class="user-mobile-detail">${u.type} · ${u.expires}<br>${u.photos} фото · ${u.videos} видео · до ${u.quality}p</div></td><td class="user-col-type">${u.type}<small>${u.role}</small></td><td class="user-col-status"><span class="status ${u.active ? "good" : "muted"}">${icon(u.active ? "circle-check" : "pause")}${u.active ? "Активен" : "Приостановлен"}</span></td><td class="user-col-quota number">${u.photos} фото · ${u.videos} видео<small>Базовое видео до ${u.quality}p</small></td><td class="user-col-priority">${u.priority}</td><td class="user-col-expiry"><span class="${u.expires.includes("5 ч.") ? "status warning" : ""}">${u.expires}</span></td><td class="user-col-edit">${iconButton("edit-user", "Изменить права: " + u.name, "sliders-horizontal", `data-index="${index}"`)}</td></tr>`).join("")}</tbody></table></div><div class="table-footer"><span>Показано ${users.length} из ${state.users.length} пользователей</span><span>Время сервера: 14:35 МСК</span></div>`
    : `<div class="empty">${icon("users")}<h2>Пользователи не найдены</h2><p>Проверьте имя или выбранный тип учётной записи.</p></div>`;
  icons();
}

function renderJobs() {
  main.innerHTML =
    heading(
      "Задачи",
      "Генерации и обучение в одном списке",
      `<span class="status good">${icon("radio")}Обновлено сейчас</span>`,
    ) +
    `
    <div class="tabs"><button aria-selected="true">Все задачи</button></div>
    <div class="jobs-list"><article class="job-row">${photoMarkup(photos[0], "job-thumb")}<div><h3>Портрет с цветным светом</h3><small>Krea2 Turbo · 4 изображения</small></div><div class="job-state"><span class="status info">${icon("image")}Генерация · 62%</span><div class="job-progress" role="progressbar" aria-label="Прогресс генерации" aria-valuenow="62" aria-valuemin="0" aria-valuemax="100"><span></span></div><small>Изображение 3 из 4</small></div>${iconButton("job-info", "Открыть задачу генерации", "chevron-right")}</article>
    <article class="job-row">${photoMarkup(photos[2], "job-thumb")}<div><h3>Обучение «Студийный свет»</h3><small>Krea2 Raw · 1200 шагов</small></div><div class="job-state"><span class="status ${state.cancelled ? "muted" : "warning"}">${icon(state.cancelled ? "circle-x" : "clock-3")}${state.cancelled ? "Отменено" : "В очереди · следующая"}</span><small>${state.cancelled ? "Оборудование не занималось" : "Перед вами 1 задача. Время уточняется."}</small></div>${iconButton(state.cancelled ? "undo-cancel" : "cancel-job", state.cancelled ? "Вернуть демонстрационную задачу" : "Отменить обучение", state.cancelled ? "rotate-ccw" : "x")}</article>
    <article class="job-row">${photoMarkup(photos[1], "job-thumb")}<div><h3>Дорога среди скал</h3><small>Flux2 · 1 изображение</small></div><div class="job-state"><span class="status good">${icon("circle-check")}Готово</span><small>Сегодня, 13:58 · 2 мин. 16 сек.</small></div>${iconButton("completed-job", "Открыть готовый результат", "chevron-right")}</article></div>`;
}

function render() {
  const names = {
    studio: "Создать",
    library: "Результаты",
    training: "Мои LoRA",
    users: "Пользователи",
    jobs: "Задачи",
  };
  state.page = Object.hasOwn(names, location.hash.slice(1))
    ? location.hash.slice(1)
    : "studio";
  $("#crumb").textContent = names[state.page];
  $$("[data-page]").forEach((link) =>
    link.toggleAttribute("aria-current", false),
  );
  $$(`[data-page="${state.page}"]`).forEach((link) =>
    link.setAttribute("aria-current", "page"),
  );
  ({
    studio: renderStudio,
    library: renderLibrary,
    training: renderTraining,
    users: renderUsers,
    jobs: renderJobs,
  })[state.page]();
  document.title = `${names[state.page]} · AI Gateway · концепт`;
  icons();
}

function navigate(page) {
  if (location.hash === "#" + page) render();
  else location.hash = page;
}

window.addEventListener("hashchange", () => {
  render();
  window.scrollTo(0, 0);
});

function setTheme(value) {
  try {
    localStorage.setItem("nd-design-theme", value);
  } catch (_) {}
  $("#theme").value = value;
  document.documentElement.dataset.theme =
    value === "system"
      ? matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light"
      : value;
}
try {
  $("#theme").value = localStorage.getItem("nd-design-theme") || "system";
} catch (_) {}
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if ($("#theme").value === "system") setTheme("system");
});

document.addEventListener("input", (event) => {
  const el = event.target;
  if (el.id === "prompt") {
    state.prompt = el.value;
    const saved = write("nd-design-prompt", state.prompt);
    $("#prompt-length").textContent = el.value.length + " символов";
    $("#draft-save").textContent = saved
      ? "Сохранено локально"
      : "Не удалось сохранить";
  } else if (el.id === "caption") {
    state.captions[state.captionIndex] = el.value;
    $("#caption-save").textContent = write("nd-design-captions", state.captions)
      ? "Сохранено локально"
      : "Не удалось сохранить";
  } else if (el.id === "library-search") {
    state.search = el.value;
    updateLibrary();
  } else if (el.id === "user-search") {
    state.userSearch = el.value;
    updateUsers();
  }
});

document.addEventListener("change", (event) => {
  const el = event.target;
  if (el.id === "theme") setTheme(el.value);
  else if (el.name === "video-mode") {
    state.mode = el.value;
    render();
  } else if (el.hasAttribute("data-processing"))
    $(`[data-options="${el.id}"]`).hidden = !el.checked;
  else if (el.id === "lora-model")
    $("#lora-weight").hidden = el.value === "none";
  else if (el.id === "model" && state.kind === "edit") {
    state.editModel = el.value;
    render();
  }
  if (["duration", "quality", "aspect", "batch"].includes(el.id)) {
    $("#submit-summary").textContent =
      state.kind === "video"
        ? `${$("#duration").value} сек. · ${$("#quality").value}p`
        : `${$("#batch").value} изобр. · ${$("#aspect").value}`;
  }
});

document.addEventListener("submit", (event) => {
  event.preventDefault();
  if (event.target.id === "generation-form") {
    showDialog(
      "Предпросмотр запуска",
      `<span class="status info">${icon("info")}Демонстрационный режим</span><p>В рабочем приложении здесь задача будет отправлена в очередь. Этот макет не запускает генерацию и не расходует лимиты.</p><dl class="key-values"><div><dt>Модель</dt><dd>${escapeHTML($("#model").value)}</dd></div><div><dt>Параметры</dt><dd>${escapeHTML($("#submit-summary").textContent)}</dd></div><div><dt>LoRA</dt><dd>${escapeHTML($("#lora-model").selectedOptions[0].textContent)}</dd></div><div><dt>SageAttention</dt><dd>${$("#sage").checked ? "Включён" : "Выключен"}</dd></div></dl>`,
      button("close", "Вернуться", "", "") +
        button("show-jobs", "Посмотреть очередь", "list-video", "primary"),
    );
  }
});

const actions = {};
document.addEventListener("click", (event) => {
  const el = event.target.closest("[data-action]");
  if (!el || el.disabled) return;
  actions[el.dataset.action]?.(el);
});

Object.assign(actions, {
  close: closeDialog,
  new: () => navigate("studio"),
  library: () => navigate("library"),
  kind: (el) => {
    state.kind = el.dataset.kind;
    state.mobile = "settings";
    render();
  },
  "mobile-view": (el) => {
    state.mobile = el.dataset.view;
    render();
  },
  "select-result": (el) => {
    state.activePhoto = Number(el.dataset.index);
    render();
  },
  filter: (el) => {
    state.filter = el.dataset.filter;
    render();
  },
  "reset-filter": () => {
    state.filter = "all";
    state.search = "";
    render();
  },
  "user-filter": (el) => {
    state.userFilter = el.dataset.filter;
    render();
  },
  "training-tab": (el) => {
    state.trainingTab = el.dataset.tab;
    render();
  },
  "caption-photo": (el) => {
    state.captionIndex = Number(el.dataset.index);
    render();
  },
  "caption-prev": () => {
    state.captionIndex = 0;
    render();
  },
  "caption-next": () => {
    state.captionIndex = 1;
    render();
  },
  theme: () =>
    setTheme(
      document.documentElement.dataset.theme === "dark" ? "light" : "dark",
    ),
  animate: () => usePhoto("video"),
  "edit-result": () => usePhoto("edit"),
  "show-jobs": () => {
    closeDialog();
    navigate("jobs");
  },
  "cancel-job": () =>
    showDialog(
      "Отменить обучение?",
      "<p>Задача ещё не запускалась. Датасет и описания останутся, обучение можно будет запустить заново.</p>",
      button("close", "Оставить в очереди", "") +
        button("confirm-cancel", "Отменить обучение", "x", "danger"),
    ),
  "confirm-cancel": () => {
    state.cancelled = true;
    closeDialog();
    render();
    toast("Демонстрационная задача отменена");
  },
  "undo-cancel": () => {
    state.cancelled = false;
    render();
  },
  "completed-job": () => {
    state.activePhoto = 1;
    state.mobile = "result";
    navigate("studio");
  },
  "job-info": () =>
    showDialog(
      "Портрет с цветным светом",
      '<p>Изображение 3 из 4. Промпт-ассистент, параметры и результаты принадлежат одной задаче.</p><div class="job-progress"><span></span></div><span class="status info">Генерация · 62%</span>',
      button("close", "Закрыть", ""),
    ),
  upload: (el) => {
    uploadSlot = Number(el.dataset.slot);
    $("#file-upload").click();
  },
  "remove-ref": (el) => {
    state.refs[Number(el.dataset.index)] = null;
    render();
  },
  picker: (el) => {
    state.pickerSlot = Number(el.dataset.slot);
    showDialog(
      "Выбрать из результатов",
      `<p>Референс ${state.pickerSlot + 1}</p><div class="library-grid">${photos.map((p, i) => mediaTile(p, i, true)).join("")}</div>`,
      "",
      true,
    );
  },
  "pick-photo": (el) => {
    const p = photos[Number(el.dataset.index)];
    state.refs[state.pickerSlot] = { src: "assets/" + p.file, alt: p.alt };
    closeDialog();
    render();
    toast("Изображение добавлено в референсы");
  },
  "view-photo": (el) => {
    state.activePhoto = Number(el.dataset.index);
    const p = photos[state.activePhoto];
    showDialog(
      p.name,
      `${photoMarkup(p, "preview-image", false)}<div class="row spread small muted"><span>${p.model} · ${p.size}</span><span>Сегодня, 14:32</span></div>`,
      button("download", "Скачать", "download") +
        button("animate", "Сделать видео", "film", "primary"),
      true,
    );
  },
  "media-menu": (el) => {
    state.activePhoto = Number(el.dataset.index);
    showDialog(
      photos[state.activePhoto].name,
      `<div class="dialog-menu">${button("animate", "Сделать видео", "film")}${button("edit-result", "Редактировать", "sliders-horizontal")}${button("favorite", "Добавить в избранное", "star")}${button("pin", "Закрепить на 30 дней", "pin")}${button("result-info", "Параметры", "info")}</div>`,
    );
  },
  favorite: () => {
    photos[state.activePhoto].favorite = !photos[state.activePhoto].favorite;
    toast(
      photos[state.activePhoto].favorite
        ? "Добавлено в избранное. Срок хранения не изменён."
        : "Убрано из избранного",
    );
  },
  pin: () => {
    showDialog(
      "Закрепить результат",
      "<p>В рабочем приложении закрепление продлевает хранение результата до 30 дней. Добавление в избранное не меняет срок хранения.</p>",
      button("close", "Отмена", "") +
        button("confirm-pin", "Закрепить", "pin", "primary"),
    );
  },
  "confirm-pin": () => {
    closeDialog();
    toast("Закреплено в макете. Файлы на сервере не изменялись.");
  },
  download: () => {
    const link = document.createElement("a");
    link.href = "assets/" + photos[state.activePhoto].file;
    link.download = photos[state.activePhoto].file;
    link.click();
  },
  repeat: () =>
    toast(
      "В рабочем приложении повтор восстановит seed, LoRA, референсы и обработку. Здесь показан макет.",
    ),
  "result-info": () =>
    showDialog(
      "Параметры результата",
      `<dl class="key-values"><div><dt>Модель</dt><dd>${photos[state.activePhoto].model}</dd></div><div><dt>Разрешение</dt><dd>${photos[state.activePhoto].size}</dd></div><div><dt>Seed</dt><dd>73192804</dd></div><div><dt>Шаги</dt><dd>8</dd></div><div><dt>LoRA</dt><dd>Не использовалась</dd></div><div><dt>Обработка</dt><dd>Выключена</dd></div></dl><h3>Промпт</h3><p>${escapeHTML(state.prompt)}</p><span class="field-hint">Демонстрационные параметры, не метаданные фотографии.</span>`,
      button("close", "Закрыть", ""),
    ),
  "video-guide": () =>
    showDialog(
      "Какой режим видео выбрать",
      "<h3>По описанию и кадрам</h3><p>Без изображений: видео по тексту. Первый кадр задаёт начало, второй задаёт конец. В workflow это ветка FL2VA.</p><h3>По референсам</h3><p>Ветка REF2VA: до четырёх фото, аудио и видео. Опишите, что брать из каждого референса. Они не обязаны быть точным первым и последним кадром.</p><h3>Качество и обработка</h3><p>Базовое качество ограничивает только генерацию. RIFE и апскейл до 2× включаются отдельно. SageAttention и другие поддерживаемые оптимизации опциональны.</p>",
      button("close", "Понятно", "", "primary"),
    ),
  audio: () =>
    showDialog(
      "Аудиореференс",
      '<p>В режиме REF2VA здесь выбирается аудиофайл, начальное смещение и роль: голос или звук сцены.</p><div class="fields-2">' +
        inputField("audio-start", "Начало, секунд", "0.03") +
        selectField(
          "audio-role",
          "Роль аудио",
          ["Голос", "Звук сцены"],
          "Голос",
        ) +
        "</div>",
      button("close", "Закрыть", ""),
    ),
  "video-ref": () =>
    showDialog(
      "Видеореференс",
      "<p>В режиме REF2VA здесь выбирается видео и диапазон кадров, которые задают движение и композицию.</p>" +
        inputField("frame-start", "Начальный кадр", "0"),
      button("close", "Закрыть", ""),
    ),
  "storage-info": () =>
    showDialog(
      "Хранение результатов",
      "<h3>24 часа</h3><p>Обычная история и её файлы удаляются через сутки. Избранное помогает сортировать, но не продлевает хранение.</p><h3>30 дней</h3><p>Закрепление продлевает хранение. Дата удаления должна быть видна в деталях результата.</p>",
      button("close", "Понятно", "", "primary"),
    ),
  notifications: () =>
    showDialog(
      "Уведомления",
      '<div class="row"><span class="status good">' +
        icon("circle-check") +
        '4 изображения готовы</span></div><p>Портрет с цветным светом · 14:32</p><hr><div class="status warning">' +
        icon("clock-3") +
        "Обучение ожидает GPU</div><p>Студийный свет · следующая задача</p>",
      button("show-jobs", "Все задачи", "list-video", "primary"),
    ),
  more: () =>
    showDialog(
      "Пространство",
      `<div class="dialog-menu"><a href="#training" data-action="close">${icon("scan-face")}Мои LoRA</a><a href="#users" data-action="close">${icon("users")}Пользователи</a>${button("profile", "Настройки", "settings")}${button("theme", "Сменить тему", "sun-moon")}</div>`,
    ),
  profile: () =>
    showDialog(
      "Настройки",
      "<h3>Алексей</h3><p>Администратор · постоянная учётная запись</p>" +
        selectField(
          "profile-theme",
          "Цветовая тема",
          [
            ["system", "Как в системе"],
            ["light", "Светлая"],
            ["dark", "Тёмная"],
          ],
          $("#theme").value,
        ),
      button("save-profile", "Сохранить", "check", "primary"),
    ),
  "save-profile": () => {
    setTheme($("#profile-theme").value);
    closeDialog();
    toast("Тема сохранена");
  },
});

function usePhoto(kind) {
  const p = photos[state.activePhoto];
  state.refs[0] = { src: "assets/" + p.file, alt: p.alt };
  state.kind = kind;
  state.mode = "exact";
  state.mobile = "settings";
  if (dialog.open) closeDialog();
  navigate("studio");
  toast("Результат добавлен первым референсом");
}

$("#file-upload").addEventListener("change", (event) => {
  const file = event.target.files[0];
  if (!file) return;
  state.refs[uploadSlot] = { src: URL.createObjectURL(file), alt: file.name };
  event.target.value = "";
  render();
  toast("Файл открыт только в этом браузере. На сервер не отправлялся.");
});

Object.assign(actions, {
  assistant: () => {
    const count =
      state.kind === "image"
        ? 0
        : state.kind === "video" && state.mode === "exact"
          ? 2
          : state.kind === "edit" && $("#model").value === "Krea2 Edit"
            ? 2
            : 4;
    const activeRefs = state.refs
      .slice(0, count)
      .map((ref, index) => ({ ref, index }))
      .filter(({ ref }) => ref);
    const hasRefs = activeRefs.length > 0;
    showDialog(
      "Помощь с промптом",
      `<div class="row spread"><span class="tag">${state.kind === "video" ? (state.mode === "free" ? "MiniMax H3 · REF2VA" : "MiniMax H3 · кадры") : state.kind === "edit" ? "Редактирование" : "Krea2 · изображение"}</span><span class="small muted">Пример ответа</span></div>${hasRefs ? '<h3>Учтённые референсы</h3><ul class="observations">' + activeRefs.map(({ ref, index }) => `<li>Фото ${index + 1}: ${escapeHTML(ref.alt)}</li>`).join("") + "</ul>" : "<p>Генерация по тексту. Изображения не переданы.</p>"}<div class="field"><label for="assistant-draft">Предложенный промпт</label><textarea id="assistant-draft" class="new-prompt">${escapeHTML(state.prompt + (state.kind === "video" ? " Камера медленно приближается. Сохранить внешний вид объекта и освещение, плавное естественное движение." : " Чёткий акцент на объекте, аккуратная композиция, детали без избыточной резкости."))}</textarea></div>`,
      button("close", "Оставить мой", "") +
        button("apply-prompt", "Применить", "check", "primary"),
    );
  },
  "apply-prompt": () => {
    state.prompt = $("#assistant-draft").value;
    write("nd-design-prompt", state.prompt);
    closeDialog();
    render();
    toast("Промпт применён к текущему черновику");
  },
  "caption-assistant": () =>
    showDialog(
      "Описание выбранного кадра",
      `${photoMarkup(state.captionIndex ? photos[2] : photos[0], "preview-image")}<span class="tag">Кадр 0${state.captionIndex + 1} · пример ответа</span><div class="field"><label for="caption-draft">Проверить описание</label><textarea id="caption-draft" class="new-prompt">${escapeHTML(state.captions[state.captionIndex])}</textarea></div>`,
      button("close", "Оставить моё", "") +
        button("apply-caption", "Применить", "check", "primary"),
    ),
  "apply-caption": () => {
    const value = $("#caption-draft")
      .value.replace(/^(nd_light[\s,]*)+/i, "")
      .trim();
    state.captions[state.captionIndex] = "nd_light, " + value;
    write("nd-design-captions", state.captions);
    closeDialog();
    render();
    toast("Описание сохранено, триггер добавлен один раз");
  },
  "training-help": () =>
    showDialog(
      "Обучение LoRA",
      "<p>Для персонажа нужны разные кадры одного человека. Для стиля можно использовать разные объекты с общей визуальной манерой.</p><h3>Материалы и подписи</h3><p>Каждый кадр описывается отдельно. Триггер-слово добавляется в начало. Проверьте волосы, одежду, ракурс и фон до запуска.</p><h3>Проверка результата</h3><p>Сравните генерации с LoRA и без неё на одном seed. Проверяйте не только объект, но и фон, цвет и мелкие детали.</p>",
      button("close", "Понятно", "", "primary"),
    ),
  "dataset-upload": () =>
    showDialog(
      "Добавить материалы",
      "<p>В рабочем редакторе сюда можно загрузить фотографии с устройства или выбрать свои результаты. В концепте показан пример датасета из двух кадров.</p>",
      button("close", "Вернуться к датасету", "", "primary"),
    ),
  "dataset-menu": () =>
    showDialog(
      "Студийный свет",
      `<div class="dialog-menu">${button("export-captions", "Экспортировать описания", "download")}${button("training-help", "Рекомендации к материалам", "circle-help")}</div>`,
    ),
  "export-captions": () => {
    const blob = new Blob(
      [
        state.captions
          .map((caption, i) => `Frame ${i + 1}\n${caption}`)
          .join("\n\n"),
      ],
      { type: "text/plain;charset=utf-8" },
    );
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "studio-light-captions.txt";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    toast("Описания экспортированы");
  },
  "training-setup": () =>
    showDialog(
      "Настройки обучения",
      `<div class="status good">${icon("circle-check")}Датасет: Студийный свет · 2 кадра</div>${selectField("training-model", "Базовая модель", ["Krea2 Raw", "Flux2 Klein"], "Krea2 Raw")}<div class="fields-2">${selectField("resolution", "Рабочее разрешение", ["512", "768", "1024"], "768")}${inputField("training-steps", "Шаги", "1200", "number", 'min="100" max="5000"')}</div><div class="fields-2">${selectField("rank", "Rank", ["8", "16", "32"], "16")}${inputField("lr", "Learning rate", "0.0001", "number", 'step="0.00001"')}</div><div class="hint-inline">${icon("clock-3")}<span>Задача будет ждать свободную GPU. Майнинг приостанавливается только на время работы.</span></div>`,
      button("close", "К датасету", "") +
        button(
          "training-preview",
          "Проверить запуск",
          "arrow-right",
          "primary",
        ),
    ),
  "training-preview": () =>
    showDialog(
      "Предпросмотр обучения",
      '<span class="status info">' +
        icon("info") +
        "Демонстрационный режим</span><p>Обучение не запущено. В рабочей версии здесь появится проверка материалов, совместимости базовой модели, ресурсов и прав пользователя.</p>",
      button("close", "Закрыть", "") +
        button("show-jobs", "Посмотреть очередь", "list-video", "primary"),
    ),
  "use-lora": () => {
    state.kind = "image";
    state.mobile = "settings";
    navigate("studio");
    toast(
      "Выберите LoRA в разделе параметров. Макет не меняет рабочие модели.",
    );
  },
  "delete-lora": () =>
    showDialog(
      "Удалить LoRA?",
      "<p>В рабочей версии удаляются файл LoRA и её запись. Датасет остаётся. Действие доступно владельцу и администратору.</p><p>В этом макете реальные файлы не затрагиваются.</p>",
      button("close", "Отмена", "") +
        button("demo-delete", "Удалить в макете", "trash-2", "danger"),
    ),
  "demo-delete": () => {
    closeDialog();
    toast("Подтверждение показано. Рабочие LoRA не удалялись.");
  },
  "edit-user": (el) => {
    state.selectedUser = Number(el.dataset.index);
    userDialog(false);
  },
  invite: () => userDialog(true),
  "save-user": () => {
    const form = $("#access-form");
    if (!form.reportValidity()) return;
    const u = state.users[state.selectedUser];
    u.photos = Number($("#access-photos").value);
    u.videos = Number($("#access-videos").value);
    u.quality = $("#access-quality").value;
    u.priority = $("#access-priority").value;
    u.train = $("#access-training").checked;
    u.active = $("#access-active").checked;
    closeDialog();
    render();
    toast("Права обновлены в демонстрационной таблице");
  },
  "create-invite": () => {
    if (!$("#access-form").reportValidity()) return;
    showDialog(
      "Приглашение · предпросмотр",
      '<span class="status info">' +
        icon("info") +
        "Демонстрационный режим</span><p>Настройки проверены. Рабочее приглашение не создавалось, ссылка не отправлялась.</p>",
      button("close", "Закрыть", "", "primary"),
    );
  },
});

function userDialog(invite) {
  const u = invite
    ? {
        photos: 20,
        videos: 3,
        quality: "720",
        priority: "Обычный",
        train: false,
        active: true,
      }
    : state.users[state.selectedUser];
  showDialog(
    invite ? "Пригласить пользователя" : `Права: ${u.name}`,
    `<form id="access-form" class="stack">${invite ? selectField("invite-template", "Шаблон доступа", ["Пробный доступ", "Автор", "Только изображения"], "Пробный доступ") : `<p>${u.login} · ${u.type}</p>`}<h3>Лимиты генераций</h3><div class="fields-2">${inputField("access-photos", "Изображения", u.photos, "number", 'min="0" max="10000" required')}${inputField("access-videos", "Видео", u.videos, "number", 'min="0" max="10000" required')}</div><div class="fields-2">${selectField(
      "access-quality",
      "Базовое качество видео",
      [
        ["480", "480p"],
        ["720", "720p"],
        ["1080", "1080p"],
        ["1440", "1440p · 2K"],
      ],
      u.quality,
    )}${selectField("access-priority", "Приоритет очереди", ["Обычный", "Высокий"], u.priority)}</div><div class="hint-inline">${icon("info")}<span>Лимит качества не ограничивает апскейлер. Доступен масштаб до 2×.</span></div>${check("access-training", "Обучение LoRA", "", u.train)}${check("access-advanced", "Точные настройки генерации", "", true)}${invite ? `<h3>Сроки</h3><div class="fields-2">${selectField("invite-link-expiry", "Приглашение действует", ["24 часа", "7 дней"], "24 часа")}${selectField("invite-account-expiry", "Удалить учётную запись", ["Через 3 дня", "Через 7 дней", "Не удалять"], "Через 3 дня")}</div>` : check("access-active", "Доступ активен", "", u.active)}</form>`,
    button("close", "Отмена", "") +
      button(
        invite ? "create-invite" : "save-user",
        invite ? "Создать приглашение" : "Сохранить права",
        "check",
        "primary",
      ),
  );
}

document.addEventListener("change", (event) => {
  if (event.target.id !== "invite-template") return;
  const template = event.target.value;
  $("#access-photos").value = template === "Автор" ? 100 : 20;
  $("#access-videos").value =
    template === "Только изображения" ? 0 : template === "Автор" ? 15 : 3;
  $("#access-training").checked = template === "Автор";
});

render();
