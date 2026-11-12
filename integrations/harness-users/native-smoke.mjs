import {createRequire} from 'node:module';
import {inspect} from 'node:util';
try { console.log(createRequire('/opt/harness/package.json')('node-addon-require-builtin').getBindingInfo()); }
catch(error){console.error(inspect(error,{depth:10}));process.exitCode=1;}
