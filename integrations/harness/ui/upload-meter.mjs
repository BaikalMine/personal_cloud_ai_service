export function createUploadMeter({ now = () => performance.now() } = {}) {
  const samples = [{ at:now(), loaded:0 }];
  let lastLoaded = 0;
  return {
    sample(loaded, total) {
      const at = now();
      loaded = Math.max(lastLoaded, Math.min(total, Math.max(0, loaded)));
      lastLoaded = loaded;
      samples.push({ at, loaded });
      while (samples.length > 2 && at - samples[1].at > 5000) samples.shift();
      const oldest = samples[0], seconds = (at - oldest.at) / 1000;
      const speed = seconds >= 0.25 ? Math.max(0,(loaded - oldest.loaded) / seconds) : null;
      return { loaded, total, percent:total ? Math.min(100,Math.floor(loaded / total * 100)) : 0,
        speed, remaining:loaded >= total ? 0 : speed > 0 ? Math.ceil((total-loaded)/speed) : null, saving:loaded >= total };
    },
  };
}

export function uploadMetricsText(stats, russian = true) {
  if (stats.saving) return russian ? 'Сохраняем файл…' : 'Saving file…';
  let rate = russian ? 'Считаем скорость…' : 'Measuring speed…';
  if (stats.speed !== null) {
    const mb = stats.speed / 1048576;
    rate = mb >= 1 ? `${mb.toFixed(1)} ${russian ? 'МБ/с' : 'MB/s'}` : `${(stats.speed/1024).toFixed(0)} ${russian ? 'КБ/с' : 'KB/s'}`;
  }
  if (stats.remaining === null) return rate;
  const remaining = stats.remaining, hours = Math.floor(remaining/3600), minutes = Math.floor(remaining%3600/60), seconds = remaining%60;
  const time = hours ? `${hours}:${String(minutes).padStart(2,'0')}:${String(seconds).padStart(2,'0')}` : `${minutes}:${String(seconds).padStart(2,'0')}`;
  return `${rate} · ${russian ? 'осталось' : 'remaining'} ${time}`;
}
