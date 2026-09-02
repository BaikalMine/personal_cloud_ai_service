(function bootstrapGenerationSummary(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.summary = api;
})(typeof window !== "undefined" ? window : null, function generationSummaryFactory() {
  const sourceLabels = {
    device: "с устройства",
    gallery: "из галереи",
    restored: "из сохранённого задания",
    unknown: "источник не указан",
  };

  const compactText = (value, fallback = "") => String(value || fallback).trim();
  const plural = (count, one, few, many) => {
    const value = Math.abs(Number(count) || 0) % 100;
    const last = value % 10;
    if (value > 10 && value < 20) return many;
    if (last === 1) return one;
    if (last > 1 && last < 5) return few;
    return many;
  };

  const materialSummary = (references = [], { hasAudio = false, hasVideo = false } = {}) => {
    const images = references.filter((item) => item && Number(item.number) > 0);
    const parts = [];
    if (images.length) {
      const sources = images.reduce((counts, item) => {
        const source = compactText(item.source, "unknown");
        counts[source] = (counts[source] || 0) + 1;
        return counts;
      }, {});
      const sourceText = Object.entries(sources)
        .map(([source, count]) => `${count} ${sourceLabels[source] || sourceLabels.unknown}`)
        .join(", ");
      parts.push(`${images.length} ${plural(images.length, "фото", "фото", "фото")} (${sourceText})`);
    }
    if (hasVideo) parts.push("видеореференс");
    if (hasAudio) parts.push("аудиореференс");
    return parts.length ? parts.join(" · ") : "Без исходных файлов";
  };

  const guideFor = ({ family = "", templateID = "", videoMode = "frames", references = [], hasAudio = false, hasVideo = false } = {}) => {
    const count = references.length;
    if (family === "minimax_h3") {
      if (videoMode === "references") {
        return {
          eyebrow: "REF2VA · свободные референсы",
          title: "Видео по промту и выбранным ориентирам",
          description: "Промт задаёт действие, а фото, видео и аудио помогают сохранить конкретные признаки. Все файлы необязательны.",
          facts: [
            { label: "Материалы", value: materialSummary(references, { hasAudio, hasVideo }) },
            { label: "Фото", value: "До четырёх, у каждого своя роль" },
            { label: "Промт", value: "Действие, камера, сцена и звук" },
          ],
          advice: count || hasAudio || hasVideo
            ? "Проверьте роли файлов: ассистент использует тот же порядок, который увидит модель."
            : "Начните с промта или добавьте только те референсы, признаки которых действительно нужно сохранить.",
        };
      }
      if (count >= 2) {
        return {
          eyebrow: "FL2VA · первый и последний кадры",
          title: "Переход между двумя точными кадрами",
          description: "Первое фото задаёт начало ролика, второе — финал. Модель строит достижимое движение между ними.",
          facts: [
            { label: "Фото 1", value: "Точный первый кадр" },
            { label: "Фото 2", value: "Точный последний кадр" },
            { label: "Промт", value: "Один последовательный переход" },
          ],
          advice: "Не описывайте резкую смену сцены, если начальный и финальный кадры визуально не связаны.",
        };
      }
      if (count === 1) {
        return {
          eyebrow: "I2VA · первый кадр",
          title: "Видео начинается с выбранного фото",
          description: "Фото фиксирует композицию первого кадра. Дальнейшее движение, камера и звук задаются промтом.",
          facts: [
            { label: "Фото 1", value: "Точный первый кадр" },
            { label: "Финал", value: "Определяет модель" },
            { label: "Промт", value: "Действие после первого кадра" },
          ],
          advice: "Опишите первое заметное движение сразу после стартового кадра.",
        };
      }
      return {
        eyebrow: "T2VA · текст в видео",
        title: "Видео полностью по вашему описанию",
        description: "Исходные файлы не нужны. Модель сама определит композицию, поэтому промт должен описывать сцену целиком.",
        facts: [
          { label: "Материалы", value: "Не нужны" },
          { label: "Промт", value: "Герой, действие, камера, свет и звук" },
          { label: "Кадры", value: "Начало и финал определяет модель" },
        ],
        advice: "Для первой проверки используйте 5 секунд и 480p, затем повышайте качество удачного варианта.",
      };
    }

    if (templateID === "image-to-image") {
      const flux = family === "flux2";
      return {
        eyebrow: flux ? "Точное редактирование" : "Редактирование фото",
        title: flux ? "Изменение сцены с несколькими референсами" : "Бережное изменение основного фото",
        description: flux
          ? "Фото 1 остаётся основой. Дополнительные фото передают внешность, одежду, предметы, позу, стиль или фон."
          : "Фото 1 задаёт сцену и композицию. Второе фото можно использовать как точный ориентир для нужной детали.",
        facts: [
          { label: "Материалы", value: materialSummary(references) },
          { label: "Фото 1", value: "Основная сцена и композиция" },
          { label: "Промт", value: "Опишите только нужное изменение" },
        ],
        advice: count
          ? "Назначьте роль каждому дополнительному фото, чтобы ассистент не смешивал их назначение."
          : "Сначала выберите основное фото с устройства или из галереи.",
      };
    }

    return {
      eyebrow: "Текст в изображение",
      title: family === "krea2" ? "Изображение с нуля по описанию" : "Новый кадр по промту",
      description: "Исходные файлы не используются. Результат определяется промтом, моделью и выбранным профилем качества.",
      facts: [
        { label: "Материалы", value: "Не нужны" },
        { label: "Промт", value: "Объект, действие, окружение, свет и стиль" },
        { label: "Старт", value: "Сбалансированный профиль" },
      ],
      advice: "Сначала добейтесь удачной композиции, затем добавляйте LoRA и повышайте итоговое разрешение.",
    };
  };

  const modeLabel = ({ family = "", templateID = "", videoMode = "frames", references = [] } = {}) => {
    if (family === "minimax_h3") {
      if (videoMode === "references") return "REF2VA · свободные референсы";
      if (references.length >= 2) return "FL2VA · первый и последний кадры";
      if (references.length === 1) return "I2VA · первый кадр";
      return "T2VA · текст в видео";
    }
    if (templateID === "image-to-image") return family === "flux2" ? "Точное редактирование" : "Фото и промт";
    return "Текст в изображение";
  };

  const buildSummary = ({
    family = "", templateID = "", workflowName = "", modelName = "", videoMode = "frames",
    references = [], hasAudio = false, hasVideo = false, output = "", duration = "", loraCount = 0,
    heavyOptions = [],
  } = {}) => {
    const outputText = [compactText(output), compactText(duration)].filter(Boolean).join(" · ") || "Параметры не выбраны";
    const heavy = heavyOptions.filter(Boolean);
    return {
      title: compactText(workflowName, "Текущая конфигурация"),
      facts: [
        { label: "Модель", value: compactText(modelName, "Не выбрана") },
        { label: "Режим", value: modeLabel({ family, templateID, videoMode, references }) },
        { label: "Результат", value: outputText },
        { label: "Материалы", value: materialSummary(references, { hasAudio, hasVideo }) },
        { label: "LoRA", value: loraCount ? `${loraCount} ${plural(loraCount, "подключена", "подключены", "подключено")}` : "Нет" },
      ],
      load: heavy.length ? "high" : "normal",
      impact: heavy.length
        ? `Дополнительная нагрузка: ${heavy.join(", ")}. Время и потребление видеопамяти вырастут.`
        : "Дополнительная тяжёлая обработка не включена.",
    };
  };

  return { materialSummary, guideFor, modeLabel, buildSummary };
});
