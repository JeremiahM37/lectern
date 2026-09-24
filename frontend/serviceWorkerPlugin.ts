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
 const assets=Object.keys(bundle).filter(name=>/\.(js|css)$/.test(name)).sort();
 const result=buildSync({entryPoints:['src/service-worker.ts'],bundle:true,write:false,format:'iife',target:'es2022',platform:'browser',logLevel:'silent'});
 const outputFile=result.outputFiles[0];
 if(!outputFile)throw new Error('esbuild produced no output for service-worker.ts');
 const source=outputFile.text;
 const version=createHash('sha256').update(assets.join('\n')+source).digest('hex').slice(0,12);
 this.emitFile({type:'asset',fileName:'sw.js',source:source.replaceAll('__CACHE_NAME__',JSON.stringify('lectern-react-'+version)).replaceAll('__STATIC_ASSETS__',JSON.stringify(['/','/icon.svg','/manifest.webmanifest','/fonts.css','/fonts/inter-latin.woff2','/fonts/inter-latin-ext.woff2',...assets.map(name=>'/react/'+name)]))});
}};}
