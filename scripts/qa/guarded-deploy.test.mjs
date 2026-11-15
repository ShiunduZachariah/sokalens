import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { test, after } from 'node:test';
const scratch=[];
after(()=>scratch.forEach(p=>rmSync(p,{recursive:true,force:true})));
function run({deploy=0,pre=0,post=0}={}) {
 const dir=mkdtempSync(join(tmpdir(),'sokalens-qa-guard-test-'));scratch.push(dir);
 const bin=join(dir,'bin');mkdirSync(bin);
 const log=join(dir,'commands');writeFileSync(log,'');
 writeFileSync(join(dir,'.env.qa'),'PRESERVED=value\n');
 const mock=(name,body)=>writeFileSync(join(bin,name),body,{mode:0o700});
 mock('git',`#!/bin/sh
echo "git:$*" >> "$GUARD_TEST_LOG"
if [ "$1" = rev-parse ]; then echo 1111111111111111111111111111111111111111; fi
`);
 mock('curl','#!/bin/sh\nexit 0\n');
 mock('docker',`#!/bin/sh
echo "docker:$*" >> "$GUARD_TEST_LOG"
if [ "$1" = inspect ]; then
 case "$3" in
 *'"services"'*) echo '{"services":{"postgres":{"command":["postgres"],"shm_size":67108864}}}' ;;
 *) service="\${4#sokalens-dev-}"; service="\${service%-1}"; echo "$service|image-$service|sha256:$service" ;;
 esac
fi
`);
 mock('bash',`#!/bin/sh
echo "bash:$*" >> "$GUARD_TEST_LOG"
case "$1" in
 scripts/qa/deploy.sh) echo NEW=value > .env.qa; touch "$GUARD_TEST_STATE"; exit "$GUARD_DEPLOY_EXIT" ;;
 scripts/qa/smoke.sh) if [ -f "$GUARD_TEST_STATE" ]; then exit "$GUARD_POST_EXIT"; else exit "$GUARD_PRE_EXIT"; fi ;;
esac
exit 0
`);
 const result=spawnSync('/bin/bash',[resolve('scripts/qa/guarded-deploy.sh')],{encoding:'utf8',env:{...process.env,PATH:bin+':'+process.env.PATH,QA_DEPLOY_DIR:dir,QA_RELEASE_LOCK:join(dir,'release.lock'),TMPDIR:dir,GUARD_TEST_LOG:log,GUARD_TEST_STATE:join(dir,'activated'),GUARD_DEPLOY_EXIT:String(deploy),GUARD_PRE_EXIT:String(pre),GUARD_POST_EXIT:String(post)}});
 return {...result,log:readFileSync(log,'utf8'),env:readFileSync(join(dir,'.env.qa'),'utf8')};
}
test('failed QA deployment restores the previous environment and images',()=>{
 const r=run({deploy:1});
 assert.notEqual(r.status,0,r.stdout+r.stderr);
 assert.equal(r.env,'PRESERVED=value\n');
 assert.match(r.log,/git:reset --hard 1111111111111111111111111111111111111111/);
 assert.match(r.log,/docker:compose .*postgres-runtime.json.*up -d --no-build --force-recreate/);
});
test('successful QA deployment keeps the validated runtime',()=>{
 const r=run();assert.equal(r.status,0,r.stdout+r.stderr);
 assert.equal(r.env,'NEW=value\n');assert.doesNotMatch(r.log,/git:reset/);
 assert.match(r.log,/backend-check.sh/);
});
test('a failed baseline prevents any QA deployment or rollback recreation',()=>{
 const r=run({pre:1});assert.notEqual(r.status,0);
 assert.equal(r.env,'PRESERVED=value\n');
 assert.doesNotMatch(r.log,/bash:scripts\/qa\/deploy.sh|docker:compose|git:reset/);
});
