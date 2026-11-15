import assert from 'node:assert/strict';
import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,rmSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {test,after} from 'node:test';
const dirs=[];after(()=>dirs.forEach(p=>rmSync(p,{recursive:true,force:true})));
function run({backup=0,post=0,proof=false,canary=0}={}) {
 const dir=mkdtempSync(join(tmpdir(),'sokalens-retention-activation-test-'));dirs.push(dir);
 const bin=join(dir,'bin'),backups=join(dir,'backups');mkdirSync(bin);mkdirSync(backups);
 const log=join(dir,'commands');writeFileSync(log,'');
 writeFileSync(join(dir,'.env.qa'),'EXPIRE_UNRESOLVED_PREDICTIONS=false\n');
 const dump=join(backups,'sokalens-test.dump');writeFileSync(dump,'test database archive');
 if(proof)writeFileSync(dump+'.verified',createHash('sha256').update('test database archive').digest('hex')+'  '+dump+'\n');
 const mock=(name,body)=>writeFileSync(join(bin,name),body,{mode:0o700});
 mock('id','#!/bin/sh\necho dev\n');mock('git','#!/bin/sh\necho '+('a'.repeat(40))+'\n');
 mock('docker',`#!/bin/sh
echo "docker:$*" >> "$ACTIVATION_TEST_LOG"
case "$1" in
 inspect) echo healthy ;;
 exec) case "$*" in *POSTGRES_DB*) echo sports_prediction_qa ;; *POSTGRES_USER*) echo sports ;; esac ;;
esac
`);
 mock('bash',`#!/bin/sh
echo "bash:$*" >> "$ACTIVATION_TEST_LOG"
case "$1" in
 infra/deploy/backup.sh)
  if [ "$ACTIVATION_BACKUP_EXIT" != 0 ]; then exit "$ACTIVATION_BACKUP_EXIT"; fi
  sha256sum "$BACKUP_DIR/sokalens-test.dump" > "$BACKUP_DIR/sokalens-test.dump.verified" ;;
 infra/deploy/check-retention-canary.sh) exit "$ACTIVATION_CANARY_EXIT" ;;
 infra/deploy/backend-check.sh)
  if [ -f "$ACTIVATION_TEST_STATE" ]; then exit "$ACTIVATION_POST_EXIT"; fi
  touch "$ACTIVATION_TEST_STATE" ;;
esac
exit 0
`);
 const r=spawnSync('/bin/bash',[resolve('infra/deploy/activate-backend-retention.sh'),'qa','a'.repeat(40)],{encoding:'utf8',env:{...process.env,PATH:bin+':'+process.env.PATH,BACKEND_DEPLOY_DIR:dir,BACKEND_BACKUP_DIR:backups,BACKEND_RELEASE_LOCK:join(dir,'release.lock'),ACTIVATION_TEST_LOG:log,ACTIVATION_TEST_STATE:join(dir,'checked'),ACTIVATION_BACKUP_EXIT:String(backup),ACTIVATION_POST_EXIT:String(post),ACTIVATION_CANARY_EXIT:String(canary)}});
 return {...r,env:readFileSync(join(dir,'.env.qa'),'utf8'),log:readFileSync(log,'utf8')};
}
test('a fresh dump without restore proof cannot enable deletion',()=>{
 const r=run({backup:1});assert.notEqual(r.status,0,r.stdout+r.stderr);
 assert.equal(r.env,'EXPIRE_UNRESOLVED_PREDICTIONS=false\n');assert.doesNotMatch(r.log,/docker:compose/);
});
test('verified backup precedes index maintenance and worker activation',()=>{
 const r=run();assert.equal(r.status,0,r.stdout+r.stderr);
 assert.match(r.env,/EXPIRE_UNRESOLVED_PREDICTIONS=true/);
 assert.ok(r.log.indexOf('bash:infra/deploy/backup.sh')<r.log.indexOf('bash:infra/deploy/backend-maintenance.sh'));
 assert.match(r.log,/--force-recreate worker/);
});
test('failed activation checks restore the prior worker environment',()=>{
 const r=run({post:1});assert.notEqual(r.status,0,r.stdout+r.stderr);
 assert.equal(r.env,'EXPIRE_UNRESOLVED_PREDICTIONS=false\n');
 assert.match(r.log,/docker:compose .* stop worker/);
 assert.match(r.log,/bash:infra\/deploy\/restore-expired-predictions.sh/);
 assert.equal((r.log.match(/--force-recreate worker/g)||[]).length,2);
});
test('only a fresh hash-verified restore proof permits backup reuse',()=>{
 const r=run({backup:1,proof:true});assert.equal(r.status,0,r.stdout+r.stderr);
 assert.doesNotMatch(r.log,/bash:infra\/deploy\/backup.sh/);assert.match(r.env,/EXPIRE_UNRESOLVED_PREDICTIONS=true/);
});

test('failed deployed cleanup canary restores deleted data and prior worker settings',()=>{
 const r=run({canary:1});assert.notEqual(r.status,0,r.stdout+r.stderr);
 assert.equal(r.env,'EXPIRE_UNRESOLVED_PREDICTIONS=false\n');
 assert.match(r.log,/restore-expired-predictions.sh/);
 assert.ok(r.log.indexOf('stop worker')<r.log.indexOf('bash:infra/deploy/restore-expired-predictions.sh'));
});
