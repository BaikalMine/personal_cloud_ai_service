export function uploadWorkspaceFile({ url, file, signal, onProgress, createXHR = () => new XMLHttpRequest() }) {
  return new Promise((resolve, reject) => {
    const xhr = createXHR();
    const abortError = () => new DOMException('Upload cancelled', 'AbortError');
    if (signal?.aborted) { reject(abortError()); return; }
    const abort = () => xhr.abort();
    const finish = (error, value) => { signal?.removeEventListener('abort', abort); error ? reject(error) : resolve(value); };
    xhr.open('POST', url.href);
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.upload.onprogress = event => onProgress?.(event.loaded, event.lengthComputable ? event.total : file.size);
    xhr.onload = () => {
      try {
        if (xhr.status === 413) throw Object.assign(new Error('Upload too large'), { code:'stream_upload_too_large' });
        if (xhr.status >= 500) throw Object.assign(new Error('Upload unavailable'), { code:'unavailable' });
        const result = JSON.parse(xhr.responseText);
        if (xhr.status < 200 || xhr.status >= 300 || !result.ok) {
          throw Object.assign(new Error('Upload failed'), { code:result.error?.code ?? 'unavailable' });
        }
        finish(null, result.value);
      } catch (error) { finish(error); }
    };
    xhr.onerror = () => finish(Object.assign(new Error('Upload connection failed'), { code:'unavailable' }));
    xhr.onabort = () => finish(abortError());
    signal?.addEventListener('abort', abort, { once:true });
    xhr.send(file);
  });
}
