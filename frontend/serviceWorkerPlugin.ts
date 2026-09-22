import type {Plugin} from 'vite';
import {transpileModule,ModuleKind,ScriptTarget} from 'typescript';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
export function serviceWorkerPlugin():Plugin{return {name:'typed-service-worker',generateBundle(_options,bundle){
 const assets=Object.keys(bundle).filter(name=>/\.(js|css)$/.test(name)).sort();
 const source=readFileSync('src/service-worker.ts','utf8');
 const version=createHash('sha256').update(assets.join('\n')+source).digest('hex').slice(0,12);
 const output=transpileModule(source.replace('export {};',''),{compilerOptions:{module:ModuleKind.None,target:ScriptTarget.ES2022}}).outputText;
 this.emitFile({type:'asset',fileName:'sw.js',source:output.replaceAll('__CACHE_NAME__',JSON.stringify('lectern-react-'+version)).replaceAll('__STATIC_ASSETS__',JSON.stringify(['/','/icon.svg','/manifest.webmanifest','/fonts.css','/fonts/inter-latin.woff2','/fonts/inter-latin-ext.woff2',...assets.map(name=>'/react/'+name)]))});
}};}
