import assert from 'node:assert/strict';
import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,rmSync} from 'node:fs';
import {join,resolve} from 'node:path';
import {tmpdir} from 'node:os';
import {spawnSync} from 'node:child_process';
import {test,after} from 'node:test';
const dirs=[];after(()=>dirs.forEach(p=>rmSync(p,{recursive:true,force:true})));
const feature='a'.repeat(40),qaBase='b'.repeat(40),mainBase='c'.repeat(40),qa='d'.repeat(40),main='e'.repeat(40);
function run({authorized=false,red=false,changed=false,review=false}={}){
 const dir=mkdtempSync(join(tmpdir(),'sokalens-window-test-'));dirs.push(dir);
 const bin=join(dir,'bin');mkdirSync(bin);const log=join(dir,'calls'),state=join(dir,'gh-state');writeFileSync(log,'');
 writeFileSync(join(bin,'git'),`#!/bin/sh
if [ "$1" = rev-parse ]; then echo ${feature}; fi
`,{mode:0o700});
 writeFileSync(join(bin,'gh'),`#!/usr/bin/python3
import json,os,sys,pathlib
args=sys.argv[1:]
with open(os.environ['WINDOW_TEST_LOG'],'a') as f:f.write(json.dumps(args)+'\\n')
p=pathlib.Path(os.environ['WINDOW_TEST_STATE'])
s=json.loads(p.read_text()) if p.exists() else {'qa':False,'main':False,'runs':[]}
feature='a'*40;qa_base='b'*40;main_base='c'*40;qa='d'*40;main='e'*40
red=os.environ['WINDOW_TEST_RED']=='true';changed=os.environ['WINDOW_TEST_CHANGED']=='true';review=os.environ['WINDOW_TEST_REVIEW']=='true'
def option(k):return args[args.index(k)+1] if k in args else ''
if args[0]=='api':
 print((qa if s['qa'] else ('f'*40 if changed else qa_base)) if args[1].endswith('/qa') else (main if s['main'] else main_base))
elif args[:2]==['pr','view']:
 n=args[2];head=feature if n=='1' else qa
 if option('--json')=='mergeCommit':print(qa if n=='1' else main)
 elif option('--json')=='statusCheckRollup':print('CI gates')
 else:print(json.dumps({'headRefOid':head,'reviewDecision':'REVIEW_REQUIRED' if review else '', 'statusCheckRollup':[{'name':'CI gates','status':'COMPLETED','conclusion':'FAILURE' if red else 'SUCCESS'}]}))
elif args[:2]==['pr','merge']:
 s['qa' if args[2]=='1' else 'main']=True;print('merged')
elif args[:2]==['pr','create']:print('https://github.example/test/repo/pull/2')
elif args[:2]==['run','list']:
 workflow=option('--workflow');branch=option('--branch')
 if workflow=='backend-maintenance.yml':
  if '--jq' in args:print(s['runs'][-1]['databaseId'] if s['runs'] else 0)
  else:print(json.dumps(list(reversed(s['runs']))))
 else:print(json.dumps([{'databaseId':1 if workflow=='qa-deploy.yml' else (2 if branch=='qa' else 3),'headSha':qa if branch=='qa' else main,'createdAt':'2026-11-15T03:00:00Z'}]))
elif args[:2]==['workflow','run']:
 s['runs'].append({'databaseId':4+len(s['runs']),'headSha':main,'createdAt':'2026-11-15T03:10:00Z'})
elif args[:2] not in (['run','watch'],['pr','checks']):raise SystemExit('unexpected gh command')
p.write_text(json.dumps(s))
`,{mode:0o700});
 const result=spawnSync('/bin/bash',[resolve('scripts/operations/backend-window.sh'),'test/repo','1',feature,qaBase,mainBase,new Date().toISOString()],{encoding:'utf8',timeout:10000,env:{...process.env,PATH:bin+':'+process.env.PATH,OPERATOR_MERGE_AUTHORIZED:String(authorized),BACKEND_WINDOW_STATE_DIR:join(dir,'result'),WINDOW_TEST_LOG:log,WINDOW_TEST_STATE:state,WINDOW_TEST_RED:String(red),WINDOW_TEST_CHANGED:String(changed),WINDOW_TEST_REVIEW:String(review)}});
 return {...result,calls:readFileSync(log,'utf8').trim().split('\n').filter(Boolean).map(x=>JSON.parse(x))};
}
for(const [name,options] of [['missing explicit owner authorization',{}],['red CI gates',{authorized:true,red:true}],['changed QA base',{authorized:true,changed:true}],['required owner review',{authorized:true,review:true}]]){
 test(name+' prevents all merges and dispatches',()=>{
  const r=run(options);assert.notEqual(r.status,0,r.stdout+r.stderr);
  assert.ok(!r.stderr.includes("SyntaxError"),r.stderr);
  assert.ok(!r.calls.some(c=>c[0]==='workflow'||c[1]==='merge'));
 });
}
test('authorized green release validates QA before production and waits for each distinct maintenance run',()=>{
 const r=run({authorized:true});assert.equal(r.status,0,r.stdout+r.stderr);
 const merges=r.calls.filter(c=>c[0]==='pr'&&c[1]==='merge');assert.equal(merges.length,2);
 assert.ok(merges.every(c=>c.includes('--match-head-commit')&&!c.includes('--admin')));
 const watches=r.calls.filter(c=>c[0]==='run'&&c[1]==='watch').map(c=>c[2]);
 assert.deepEqual(watches,['1','2','3','4','5']);
 const dispatches=r.calls.filter(c=>c[0]==='workflow');assert.equal(dispatches.length,2);
 assert.ok(dispatches[0].includes('target=qa'));assert.ok(dispatches[1].includes('target=production'));
});
