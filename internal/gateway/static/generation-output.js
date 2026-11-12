(function bootstrapGenerationOutput(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.output = api;
})(typeof window !== "undefined" ? window : null, function generationOutputFactory() {
  const number = (value, fallback = 0) => {
    if (value === "" || value === undefined || value === null) return fallback;
    const parsed = Number(String(value).replace(",", "."));
    return Number.isFinite(parsed) ? parsed : fallback;
  };
  const checked = value => value === true || value === "true";
  const size = (width, height) => width > 0 && height > 0 ? { width, height } : null;
  const floorSize = (frame, multiple, minimum = multiple) => frame && size(
    Math.max(minimum, Math.floor(frame.width / multiple) * multiple),
    Math.max(minimum, Math.floor(frame.height / multiple) * multiple),
  );
  // ComfyUI's Python nodes use ties-to-even, unlike Math.round in JavaScript.
  const roundEven = value => value % 1 === 0.5 ? 2 * Math.round(value / 2) : Math.round(value);
  const qwen21EditSize = (frame, megapixels = 1) => {
    if (!frame || !(frame.width > 0 && frame.height > 0)) return null;
    const target = number(megapixels, 1) > 4.27 ? 1 : number(megapixels, 1);
    let resolution = Math.min(4096, roundEven(Math.sqrt(target * 1024 * 1024) / 32) * 32);
    for (; resolution >= 256; resolution -= 32) {
      const width = roundEven(Math.sqrt(resolution * resolution * frame.width / frame.height) / 32) * 32;
      const height = roundEven(Math.sqrt(resolution * resolution * frame.height / frame.width) / 32) * 32;
      if (width >= 256 && height >= 256 && width <= 4096 && height <= 4096 && width * height <= 4477419) {
        return { width, height, resolution };
      }
    }
    return null;
  };
  const multiplySize = (frame, scale, truncate = false) => frame && size(...[frame.width, frame.height].map(value => {
    const scaled = truncate ? Math.trunc(value * scale) : value * scale;
    return Math.max(8, roundEven(scaled / 8) * 8);
  }));
  const fit = (frame, longest) => {
    if (!frame || longest <= 0 || Math.max(frame.width, frame.height) <= longest) return frame;
    const scale = longest / Math.max(frame.width, frame.height);
    return size(roundEven(frame.width * scale), roundEven(frame.height * scale));
  };

  // Mirrors generationDimensions and baseGenerationDimensions in workflows.go.
  const imageResolution = ({ width = 1024, height = 1024, aspect = "custom", megapixels = 1.9, multiple = 16, maxLongest = 0 } = {}) => {
    if (!aspect || aspect === "custom") return size(number(width), number(height));
    const [x, y] = aspect.split(":").map(Number);
    const mp = number(megapixels);
    const div = number(multiple, 16);
    if (!(x > 0 && y > 0 && mp >= 0.1 && mp <= 16 && div >= 8 && div <= 128 && !(div & (div - 1)))) return null;
    const pixels = mp * 1024 * 1024;
    let frame = size(Math.floor(Math.sqrt(pixels * x / y) / div) * div, Math.floor(Math.sqrt(pixels * y / x) / div) * div);
    if (!frame) return null;
    if (maxLongest > 0 && Math.max(frame.width, frame.height) > maxLongest) {
      const scale = maxLongest / Math.max(frame.width, frame.height);
      frame = size(Math.floor(frame.width * scale / div) * div, Math.floor(frame.height * scale / div) * div);
    }
    return frame && size(Math.max(256, frame.width), Math.max(256, frame.height));
  };
  const baseResolution = (frame, megapixels = 1) => {
    if (!frame) return null;
    const pixels = Math.trunc(number(megapixels, 1) * 1024 * 1024);
    if (frame.width * frame.height <= pixels) return frame;
    const scale = Math.sqrt(pixels / (frame.width * frame.height));
    return floorSize(size(frame.width * scale, frame.height * scale), 64, 256);
  };

  const editPresets = {
    "Instagram Portrait (4:5) - 1080x1350": [1080, 1350], "Instagram Square (1:1) - 1080x1080": [1080, 1080],
    "Widescreen (16:9) - 1344x768": [1344, 768], "TikTok (9:16) - 1080x1920": [1080, 1920],
    "CivitAI Cover (4:1) - 1600x400": [1600, 400], "2:3 (Portrait Photo) - 832x1248": [832, 1248],
    "3:2 (Photo) - 1248x832": [1248, 832], "3:4 (Portrait Standard) - 896x1152": [896, 1152],
    "4:3 (Standard) - 1152x896": [1152, 896], "21:9 (Ultrawide) - 1536x640": [1536, 640],
  };
  // AspectRatioSimplifier / LCAspectRatioPipeOut, followed by the family's latent alignment.
  const editResolution = (source, custom, values, family) => {
    if (!source) return null;
    const useCustom = checked(values.edit_use_custom_size);
    let frame = useCustom ? custom : source;
    if (useCustom && family === "flux2") {
      const preset = editPresets[values.edit_aspect_preset];
      if (preset) frame = size(...preset);
      if (checked(values.edit_swap_dimensions) && frame) frame = size(frame.height, frame.width);
    }
    const limit = number(values.max_longest_side) || (family === "krea2" ? 4096 : 2160);
    frame = floorSize(fit(frame, family === "krea2" ? Math.min(4096, limit) : limit), 8);
    const proportion = family === "krea2" ? "crop" : values.edit_proportion || "crop";
    if (frame && proportion === "total_pixels") {
      const pixels = frame.width * frame.height;
      frame = floorSize(size(Math.trunc(Math.sqrt(pixels * source.width / source.height)), Math.trunc(Math.sqrt(pixels * source.height / source.width))), 8);
    }
    if (frame && (proportion === "resize" || proportion === "total_pixels")) {
      const scale = Math.min(frame.width / source.width, frame.height / source.height);
      frame = floorSize(size(roundEven(source.width * scale), roundEven(source.height * scale)), 8);
    }
    return floorSize(frame, family === "flux2" ? 16 : 8);
  };
  const seedVR2Resolution = frame => {
    if (!frame) return null;
    // LCGetImage.longer_side * 2 is passed as SeedVR2's SHORTER-side target.
    const target = Math.max(frame.width, frame.height) * 2;
    const short = Math.min(frame.width, frame.height);
    const expanded = size(Math.trunc(frame.width * target / short), Math.trunc(frame.height * target / short));
    return fit(expanded, 4098);
  };

  const plan = ({ family = "", templateID = "", values = {}, sourceSize = null, videoSize = null } = {}) => {
    const v = values;
    let base = imageResolution({ width: v.width, height: v.height, aspect: v.aspect_ratio, megapixels: v.output_megapixels,
      multiple: v.dimension_multiple, maxLongest: number(v.max_longest_side) });
    let final = base;
    const result = { base, final, fps: null, duration: null, processing: [], referenceMP: null, approximate: false };
    if (family === "minimax_h3") {
      base = videoSize;
      final = base;
      result.fps = 24;
      result.duration = number(v.video_duration_seconds, 5);
      if (checked(v.video_long_enabled) && v.generation_workflow !== "aihub-minimax-h3") {
        result.duration = Math.min(20, Math.max(2, number(v.video_long_parts, 2))) * 15;
      }
      if (v.generation_workflow === "aihub-minimax-h3") {
        if (v.aihub_mode === "video" && checked(v.aihub_match_duration)) {
          const seconds = number(v.aihub_reference_duration, 0);
          result.duration = seconds > 0 ? Math.round(seconds * 24) / 24 : null;
          result.referenceDuration = true;
        }
        if (v.aihub_mode === "carousel") {
          if (v.aihub_carousel_pipeline === "v2") {
            const scale = base ? Math.min(1, 2048 / Math.max(base.width, base.height)) : 1;
            final = base && size(Math.floor(base.width * scale / 16) * 16, Math.floor(base.height * scale / 16) * 16);
            result.photoCountUpperBound = number(v.aihub_photo_count, 12);
            result.processing.push(v.aihub_carousel_refiner === "qwen21" ? "Qwen 2.1" : "FLUX.2");
          } else {
          const scale = number(v.aihub_latent_scale, 2);
          final = base && size(...[base.width, base.height].map(value => Math.max(32, roundEven(value * scale / 32) * 32)));
          const frames = number(v.aihub_photo_frames, 50);
          result.photoCountUpperBound = Math.max(0, Math.ceil((frames - number(v.aihub_photo_skip, 5)) / number(v.aihub_photo_stride, 8)));
          result.processing.push("H3 latent " + scale + "×");
          }
        } else if (checked(v.aihub_half_resolution)) {
          base = base && size(...[base.width, base.height].map(value => Math.max(32, roundEven(value / 64) * 32)));
          final = base;
        }
      }
      if (checked(v.video_rife_enabled) && v.generation_workflow !== "aihub-minimax-h3") {
        result.fps *= number(v.video_rife_multiplier, 2);
        result.processing.push("RIFE");
      }
      if (v.generation_workflow === "aihub-minimax-h3" && v.aihub_mode !== "carousel" && checked(v.aihub_fg_enabled)) {
        result.fps *= number(v.aihub_fg_multiplier, 2);
        result.processing.push("DLSS FG");
      }
      if (checked(v.video_rtx_enabled)) {
        const scale = number(v.video_rtx_scale, 2);
        final = multiplySize(final, scale, true);
        result.processing.push(`RTX ${scale.toLocaleString("ru-RU")}×`);
      }
    } else if (templateID === "image-to-image" && family === "qwen21") {
      if (sourceSize && checked(v.preserve_original_size)) {
        base = final = qwen21EditSize(sourceSize, v.output_megapixels);
      }
    } else if (templateID === "image-to-image" && (family === "krea2" || family === "flux2")) {
      let preserve = checked(v.preserve_original_size);
      if (family === "krea2" && base) {
        const scale = Math.min(1, Math.sqrt(4.7 * 1024 * 1024 / (base.width * base.height)), 4096 / Math.max(base.width, base.height));
        if (scale < 1) {
          base = floorSize(size(base.width * scale, base.height * scale), 8, 16);
          preserve = true;
        }
      }
      base = editResolution(sourceSize, base, v, family);
      final = base;
      if (family === "krea2") {
        const scale = preserve ? 1 : number(v.upscale_factor, 1.5);
        final = multiplySize(base, scale);
        if (scale > 1) result.processing.push(`Апскейл ${scale.toLocaleString("ru-RU")}×`);
      } else {
        result.referenceMP = number(v.source_megapixels, 1);
        if (["ultimate", "both"].includes(v.flux_upscale_mode)) {
          final = multiplySize(final, 1.5);
          result.processing.push("Ultimate 1,5×");
        }
        if (["seedvr2", "both"].includes(v.flux_upscale_mode)) {
          final = seedVR2Resolution(final);
          result.processing.push("SeedVR2");
          result.approximate = true;
        }
      }
    } else if (family === "krea2") {
      if (v.generation_workflow === "krea2-pro") {
        let settings = {}; try { settings = JSON.parse(v.krea_pro_settings || '{}'); } catch (_) {}
        if (v.krea_pro_mode === "outpaint" && base) {
          const pad = settings.nodes?.['3870'] || {};
          const w=base.width,h=base.height,a=number(pad.angle)*Math.PI/180,c=Math.abs(Math.cos(a)),s=Math.abs(Math.sin(a));
          let bw=w*c+h*s,bh=w*s+h*c;
          if(pad.fit==='crop') { const k=Math.min(w/bw,h/bh),m=Math.abs(number(pad.angle)/90-roundEven(number(pad.angle)/90))<1e-9?0:2;bw=Math.max(1,w*k-m);bh=Math.max(1,h*k-m); }
          let ow=Math.max(16,bw+(number(pad.left,10)+number(pad.right,10))*w/100),oh=Math.max(16,bh+(number(pad.top)+number(pad.bottom))*h/100);
          const ratio=pad.aspect==='original'?w/h:pad.aspect?.includes(':')?pad.aspect.split(':').map(Number).reduce((x,y)=>x/y):0;
          if(ratio>0) { if(ow/oh>ratio) oh=ow/ratio;else ow=oh*ratio; }
          const snap=Math.max(1,number(pad.snap_to,16));
          base=size(Math.ceil(roundEven(ow)/snap)*snap,Math.ceil(roundEven(oh)/snap)*snap);
          if (base && (base.width > 4096 || base.height > 4096 || base.width * base.height > 4477419)) result.error = "Уменьшите размер базы или расширение кадра";
        }
        const legacyScale = number(v.krea_pro_upscale,2);
        const upscaler = v.krea_pro_upscaler || (legacyScale === 1 ? 'none' : settings.enabled?.['4150'] ? 'relay' : settings.enabled?.['4046'] === false ? 'none' : 'vosr');
        const scale = upscaler === 'relay' ? number(v.krea_pro_relay_scale,2) : upscaler === 'none' ? 1 : legacyScale;
        final = result.error ? null : base && size(roundEven(base.width*scale),roundEven(base.height*scale));
        if (final && (final.width > 12288 || final.height > 12288 || final.width * final.height > 4477419 * 9)) {
          result.error = "Уменьшите размер базы или масштаб апскейла";
          final = null;
        }
        result.processing.push('Lonecat · полный');
        if (scale > 1) result.processing.push(`${upscaler === 'relay' ? 'RelayVSR' : 'VOSR2'} ${scale}×`);
        if (settings.enabled?.['3623']) result.approximate = true;
      } else if (v.generation_workflow === "krea2-ultra") {
        base = final;
        result.processing.push("Lonecat · два прохода");
        if (checked(v.krea_ultra_speed_boost)) result.processing.push("Speed Boost");
      } else {
        base = baseResolution(final, number(v.base_megapixels, 1));
        result.processing.push("Апскейл Krea2");
      }
    } else if (family === "moss" && checked(v.moss_seedvr2) && base) {
      const target = number(v.moss_upscale_resolution, 2560);
      const scale = target / Math.max(base.width, base.height);
      final = size(roundEven(base.width * scale), roundEven(base.height * scale));
      result.processing.push("SeedVR2 Sharp");
      result.approximate = true;
    } else if (family === "flux2") {
      base = final = floorSize(base, 16);
    }
    return { ...result, base, final };
  };

  const sizeLabel = frame => frame ? `${frame.width} × ${frame.height}` : "После выбора фото";
  const rangeLabel = values => {
    const distinct = [...new Set(values)];
    return distinct.length <= 1 ? distinct[0] : `${distinct[0]} → ${distinct.at(-1)}`;
  };
  const facts = (plans, { video = false, count = 1, variation = "" } = {}) => {
    const carousel = plans.some(item => item.photoCountUpperBound !== undefined);
    const countLabel = carousel ? "до " + count * Math.max(...plans.map(item => item.photoCountUpperBound || 0)) : String(count);
    const final = rangeLabel(plans.map(item => item.error || `${item.approximate && item.final ? "≈ " : ""}${sizeLabel(item.final)}`));
    const items = [
      { label: "Базовый размер", value: rangeLabel(plans.map(item => sizeLabel(item.base))) },
      { label: "Ожидаемый итог", value: final },
    ];
    if (video) {
      const unresolved = plans.some(item => item.referenceDuration && item.duration === null);
      items.push({ label: "Длительность (задано)", value: unresolved ? "Как у видеореференса"
        : `${rangeLabel(plans.map(item => number(item.duration).toLocaleString("ru-RU", { maximumFractionDigits: 3 })))} сек.` });
      items.push({ label: "Частота кадров", value: `${rangeLabel(plans.map(item => item.fps))} FPS` });
    }
    items.push({ label: "Количество", value: `${countLabel} ${video ? "видео" : "фото"}` });
    if (plans.some(item => item.referenceMP !== null)) items.push({ label: "Анализ референсов", value: `${rangeLabel(plans.map(item => item.referenceMP?.toLocaleString("ru-RU")))} Мп` });
    if (variation) items.push({ label: "В серии меняется", value: variation });
    return { facts: items, compact: `${countLabel} ${video ? "видео" : "фото"} · ${final}` };
  };
  return { imageResolution, baseResolution, qwen21EditSize, plan, facts, roundEven };
});
