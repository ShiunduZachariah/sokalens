import assert from 'node:assert/strict';
import test from 'node:test';
import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
const script=fileURLToPath(new URL('./ensure-results-index.sh',import.meta.url));
function run(t,initial,{fail=false,project='sports-prediction-platform'}={}) {
 const dir=mkdtempSync(join(tmpdir(),'results-index-test-'));t.after(()=>rmSync(dir,{recursive:true,force:true}));
 const bin=join(dir,'bin');mkdirSync(bin);const state=join(dir,'state'),log=join(dir,'commands');writeFileSync(state,initial);writeFileSync(log,'');
 writeFileSync(join(bin,'docker'),`#!/bin/bash
query="$(cat)"
printf '%s\n' "$query" >> "$INDEX_TEST_LOG"
if [[ "$query" == *RESULTS_COVER_INDEX_STATE* ]]; then cat "$INDEX_TEST_STATE";
elif [[ "$query" == *'DROP INDEX CONCURRENTLY'* ]]; then echo missing > "$INDEX_TEST_STATE";
elif [[ "$query" == *'CREATE INDEX CONCURRENTLY'* ]]; then
 [[ "$INDEX_TEST_FAIL" != 1 ]] || exit 1
 echo valid > "$INDEX_TEST_STATE"
elif [[ "$query" == 'ANALYZE engine_predictions' ]]; then :;
else exit 9; fi
`,{mode:0o700});
 const result=spawnSync('bash',[script,project],{encoding:'utf8',env:{...process.env,PATH:bin+':'+process.env.PATH,INDEX_TEST_STATE:state,INDEX_TEST_LOG:log,INDEX_TEST_FAIL:fail?'1':'0'}});
 return {...result,log:readFileSync(log,'utf8')};
}
test('missing results index is built concurrently from the migration',t=>{
 const r=run(t,'missing');assert.equal(r.status,0,r.stderr);assert.match(r.log,/CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_engine_predictions_results_btts_cover/);assert.match(r.log,/INCLUDE \(fixture_id,market,generated_at,prediction,confidence\)/);assert.match(r.log,/WHERE market='btts_total_goals'/);assert.match(r.stdout,/built concurrently and verified/);
});
test('a valid build is reused without DDL or analysis',t=>{
 const r=run(t,'valid');assert.equal(r.status,0,r.stderr);assert.doesNotMatch(r.log,/CREATE INDEX|DROP INDEX|ANALYZE/);
});
test('only a matching interrupted build is dropped and rebuilt online',t=>{
 const r=run(t,'invalid');assert.equal(r.status,0,r.stderr);assert.ok(r.log.indexOf('DROP INDEX CONCURRENTLY')<r.log.indexOf('CREATE INDEX CONCURRENTLY'));
});
test('conflicting definition fails without destructive replacement',t=>{
 const r=run(t,'conflict');assert.notEqual(r.status,0);assert.doesNotMatch(r.log,/DROP INDEX|CREATE INDEX/);
});
test('failed online creation stops deployment preparation',t=>{
 const r=run(t,'missing',{fail:true});assert.notEqual(r.status,0);assert.doesNotMatch(r.log,/ANALYZE/);
});
test('unknown environment is refused before any database command',t=>{
 const r=run(t,'missing',{project:'other'});assert.equal(r.status,2);assert.equal(r.log,'');
});
test('QA and production prebuild the index before recording its migration',()=>{
 const prod=readFileSync(new URL('./ci-deploy.sh',import.meta.url),'utf8');
 const qa=readFileSync(new URL('../../scripts/qa/deploy.sh',import.meta.url),'utf8');
 assert.ok(prod.indexOf('bash infra/deploy/ensure-results-index.sh')<prod.indexOf('/home/deploy/dbtool-linux -mode up'));
 assert.ok(qa.indexOf('bash infra/deploy/ensure-results-index.sh')<qa.indexOf('-mode up'));
});
