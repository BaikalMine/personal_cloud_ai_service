(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayPostprocess = api;
})(typeof window !== "undefined" ? window : null, function () {
  "use strict";
  const isJob = job => job?.operation === "postprocess" || ["postprocess-image-v1", "postprocess-video-v1"].includes(job?.template_id);
  const title = job => job?.template_id === "postprocess-video-v1" ? "Постобработка видео" : "Постобработка изображения";
  const valid = view => view?.version === 1 && view.plan?.version === 1 && ["image", "video"].includes(view.plan.media_type)
    && [view.plan.options?.width, view.plan.options?.height].every(value => Number.isSafeInteger(value) && value >= 64 && value <= 4096);
  const fps = plan => Number.isSafeInteger(plan?.fps_numerator) && Number.isSafeInteger(plan?.fps_denominator) && plan.fps_numerator > 0 && plan.fps_denominator > 0
    ? `${(plan.fps_numerator / plan.fps_denominator).toLocaleString("ru-RU", { maximumFractionDigits: 3 })}${plan.fps_denominator === 1 ? "" : ` (${plan.fps_numerator}/${plan.fps_denominator})`}` : "";
  const summary = view => valid(view) ? `План: ${view.plan.options.width} × ${view.plan.options.height}${view.plan.media_type === "video" && fps(view.plan) ? ` · ${fps(view.plan)} FPS` : ""}` : "План обработки недоступен";
  const facts = view => {
    if (!valid(view)) return [];
    const plan = view.plan, options = plan.options, source = view.sources?.[0]?.measurement;
    const result = [["Размер (задано)", `${options.width} × ${options.height}`]];
    if (Number.isSafeInteger(source?.width) && Number.isSafeInteger(source?.height)) result.unshift(["Размер исходника", `${source.width} × ${source.height}`]);
    if (options.rtx_quality) result.push(["Увеличение", "Включено"]);
    if (options.interpolate > 1) result.push(["Плавность движения", `${options.interpolate}×`]);
    if (fps(plan)) result.push(["Частота (задано), FPS", fps(plan)]);
    if (Number.isSafeInteger(plan.output_frames) && plan.media_type === "video") result.push(["Кадров (задано)", plan.output_frames]);
    if (options.color_method) result.push(["Цветовой референс", `${Math.round(options.color_strength * 100)}%`]);
    return result;
  };
  const sources = view => (valid(view) && Array.isArray(view.sources) ? view.sources.slice(0, 2) : []).map((source, index) => {
    const label = index ? "Цветовой референс" : "Исходный файл";
    const available = source.availability === "available" && typeof source.media_id === "string" && /^[1-9][0-9]{0,18}$/.test(source.media_id);
    return { label, href: available ? `/gallery?media=${encodeURIComponent(source.media_id)}` : "",
      message: source.availability === "unavailable" ? `${label} больше недоступен` : `${label}: доступность не проверена` };
  });
  const differences = (view, measured) => {
    if (!valid(view) || !measured) return [];
    const result = [], plan = view.plan;
    if (Number.isSafeInteger(measured.width) && Number.isSafeInteger(measured.height) && (measured.width !== plan.options.width || measured.height !== plan.options.height)) result.push("Размер результата отличается от заданного.");
    const ratio = typeof measured.average_fps === "string" && /^\d{1,10}\/\d{1,10}$/.test(measured.average_fps) ? measured.average_fps.split("/").map(Number) : [];
    const actualFPS = ratio.length === 2 && ratio.every(value => value > 0 && value <= 1e9) ? ratio[0] / ratio[1] : NaN;
    if (plan.media_type === "video" && fps(plan) && Number.isFinite(actualFPS) && Math.abs(actualFPS - plan.fps_numerator / plan.fps_denominator) > 0.001) result.push("Средняя частота кадров отличается от заданной.");
    return result;
  };
  return { isJob, title, valid, fps, summary, facts, sources, differences };
});
