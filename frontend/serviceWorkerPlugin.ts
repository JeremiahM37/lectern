import type {Plugin} from 'vite';
import {buildSync} from 'esbuild';
import {createHash} from 'node:crypto';
// service-worker.ts imports from ./sw-actions (docs/agent-events.md section
// 3: the approve/deny decision logic lives there so sw-actions.test.ts can
// exercise it directly, since a real ServiceWorkerGlobalScope isn't
// available to a plain Node test). A single-file transpile — this plugin's
// old approach — cannot resolve that import at all: without a module
// system, TypeScript either drops it or emits a `require(...)` no runtime
// here can satisfy, and a classic-script service worker cannot use a bare
// `import` statement either. esbuild's own bundler resolves and inlines it,
// and IIFE output keeps the emitted sw.js a plain classic script exactly
// like the previous single-file transpile did.
export function serviceWorkerPlugin():Plugin{return {name:'typed-service-worker',generateBundle(_options,bundle){
 // Precache what the pages can load: entries, their static imports and
 // their dynamic imports, with CSS. The file editor, viewers and diagram
 // renderer are the exception (large and optional): they are fetched when
 // first used and then kept by the runtime rule for /react/assets, so an
 // installed phone never downloads megabytes it may not need.
 const optional=/\/src\/files\/(monaco|viewers|RichMarkdown)\.tsx?$|\/node_modules\/(mermaid|monaco-editor|@tiptap|prosemirror-[a-z-]+)\//;
 const kept=new Set<string>();
 const visit=(name:string)=>{const item=bundle[name];if(!item||kept.has(name))return;if(item.type==='chunk'&&!item.isEntry&&item.facadeModuleId&&optional.test(item.facadeModuleId))return;kept.add(name);if(item.type!=='chunk')return;item.imports.forEach(visit);item.dynamicImports.forEach(visit);const meta=(item as {viteMetadata?:{importedCss?:Set<string>}}).viteMetadata;meta?.importedCss?.forEach(css=>kept.add(css));};
 for(const item of Object.values(bundle))if(item.type==='chunk'&&item.isEntry)visit(item.fileName);
 const assets=[...kept].filter(name=>/\.(js|css)$/.test(name)).sort();
 const result=buildSync({entryPoints:['src/service-worker.ts'],bundle:true,write:false,format:'iife',target:'es2022',platform:'browser',logLevel:'silent'});
 const outputFile=result.outputFiles[0];
 if(!outputFile)throw new Error('esbuild produced no output for service-worker.ts');
 const source=outputFile.text;
 const version=createHash('sha256').update(assets.join('\n')+source).digest('hex').slice(0,12);
 this.emitFile({type:'asset',fileName:'sw.js',source:source.replaceAll('__CACHE_NAME__',JSON.stringify('lectern-react-'+version)).replaceAll('__STATIC_ASSETS__',JSON.stringify(['/','/icon.svg','/manifest.webmanifest','/fonts.css','/fonts/inter-latin.woff2','/fonts/inter-latin-ext.woff2',...assets.map(name=>'/react/'+name)]))});
}};}
