import assert from 'node:assert/strict';
import test from 'node:test';
import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';import {join} from 'node:path';import {spawnSync} from 'node:child_process';import {fileURLToPath} from 'node:url';
const script=fileURLToPath(new URL('../../scripts/qa/smoke.sh',import.meta.url));
function run(t,failed){
 const dir=mkdtempSync(join(tmpdir(),'results-smoke-test-'));t.after(()=>rmSync(dir,{recursive:true,force:true}));const bin=join(dir,'bin');mkdirSync(bin);const log=join(dir,'requests');writeFileSync(log,'');
 writeFileSync(join(bin,'curl'),String.raw`#!/usr/bin/env python3
import os,sys
url=sys.argv[-1]
with open(os.environ['SMOKE_TEST_LOG'],'a') as f:f.write(url+'\n')
if ':18080/' in url: print('{"matches":[],"predictions":[],"items":[],"settled":0,"results":[]}\n200')
else:
 title='SokaLens Today Acca predictions In-play prediction results'
 text="<h3>This day&#x27;s results could not be loaded</h3>" if '/results?' in url and os.environ['SMOKE_TEST_FAIL']=='1' else "<h3>Nothing settled for this day yet</h3><script>const text=\"This day's results could not be loaded\";</script>"
 print('<html><title>'+title+'</title><body>'+text+'x'*6000+'</body></html>\n200')
`,{mode:0o700});
 const r=spawnSync('bash',[script],{encoding:'utf8',env:{...process.env,PATH:bin+':'+process.env.PATH,QA_WEB_ORIGIN:'http://127.0.0.1:13000',QA_API_ORIGIN:'http://127.0.0.1:18080',SMOKE_TEST_LOG:log,SMOKE_TEST_FAIL:failed?'1':'0'}});return {...r,log:readFileSync(log,'utf8')};
}
test('rendered unavailable state fails smoke even with HTTP 200',t=>{const r=run(t,true);assert.notEqual(r.status,0);assert.match(r.stdout,/settled-results request failed inside an HTTP 200 page/);});
test('legitimate empty day and bundled error text do not fail smoke',t=>{const r=run(t,false);assert.equal(r.status,0,r.stderr+r.stdout);});
test('deployment smoke exercises the previous settled day',t=>{const r=run(t,false);assert.match(r.log,/results\?day=yesterday/);assert.match(r.log,/predictions\/results\?day=yesterday&limit=20/);});
