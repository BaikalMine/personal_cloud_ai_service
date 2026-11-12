import {readFile,writeFile,copyFile} from 'node:fs/promises';
import {join} from 'node:path';
const root=process.argv[2]||'/opt/harness/node_modules';
const lib=join(root,'@deepseek-ai/dsh-tool-fs/lib');
const file=join(lib,'index.js');
const marker='import { buildReadWindow, formatReadWindow, readColumn } from "./gateway-read-continuation.mjs";';
let source=await readFile(file,'utf8');
if (!source.includes(marker)) {
  function replace(oldText,newText) {
    if(source.split(oldText).length!==2) throw new Error('Unsupported Harness read implementation; no patch written');
    source=source.replace(oldText,newText);
  }
  replace('filePath: args.file_path,\n\t\toffset,','filePath: args.file_path,\n\t\tcolumnOffset: readColumn(args.column_offset),\n\t\toffset,');
  replace('Use offset and limit to continue reading large files.', 'Use offset and limit to continue reading large files. A PARTIAL READ is not the whole document. Follow the exact offset and column_offset cursor until the required passage is fully read. After compaction, re-read relevant saved text before revising; summaries are not verbatim originals. Never reconstruct unseen passages from a summary.');
  replace('description: "Read a UTF-8 text file and return line-numbered content."','description: "Read a UTF-8 text file. Partial results include an exact continuation cursor, including within long paragraphs. Follow it before editing unseen text."');
  replace('\t\t\tlimit: {\n\t\t\t\ttype: "number",\n\t\t\t\tdescription: `Maximum number of lines to return. Defaults to ${caps.limit}.`','\t\t\tcolumn_offset: { type: "number", description: "Zero-based UTF-16 character offset within the first requested line. Defaults to 0. Copy the exact continuation cursor returned by read." },\n\t\t\tlimit: {\n\t\t\t\ttype: "number",\n\t\t\t\tdescription: `Maximum number of lines to return. Defaults to ${caps.limit}.`');
  replace('\t\t\t\t\ttotalLines: {\n\t\t\t\t\t\ttype: "integer",','\t\t\t\t\tcolumnOffset: { type: "integer" },\n\t\t\t\t\tcomplete: { type: "boolean" },\n\t\t\t\t\tnextOffset: { type: "integer" },\n\t\t\t\t\tnextColumnOffset: { type: "integer" },\n\t\t\t\t\ttotalLines: {\n\t\t\t\t\t\ttype: "integer",');
  const renderStart=source.indexOf('\t\t\trender: (args, value) => {');
  const renderEnd=source.indexOf('\n\t\t\tpresentationMeta:',renderStart);
  if(renderStart<0||renderEnd<0)throw new Error('Read renderer not found');
  source=source.slice(0,renderStart)+'\t\t\trender: (_args, value) => [{ type: "text", text: formatReadWindow(value.path, value) }],'+source.slice(renderEnd);
  replace('const window = await buildWindow(info.size', 'const window = await buildReadWindow(info.size');
  replace('\t\t\t\tmaxLineLength: caps.maxLineLength,\n\t\t\t\tmaxBytes: caps.maxBytes','\t\t\t\tcolumnOffset: input.columnOffset,\n\t\t\t\tsignal: exec.signal,\n\t\t\t\tmaxLineLength: caps.maxLineLength,\n\t\t\t\tmaxBytes: caps.maxBytes');
  replace('\t\t\t\ttotalLines: window.totalLines','\t\t\t\ttotalLines: window.totalLines,\n\t\t\t\tcolumnOffset: window.columnOffset,\n\t\t\t\tcomplete: window.complete,\n\t\t\t\tnextOffset: window.nextOffset,\n\t\t\t\tnextColumnOffset: window.nextColumnOffset');
  source=marker+'\n'+source;
} else if (!source.includes('const window = await buildReadWindow(info.size')) throw new Error('Incomplete read patch');
await copyFile(new URL('./read-continuation.mjs',import.meta.url),join(lib,'gateway-read-continuation.mjs'));
await writeFile(file,source);
