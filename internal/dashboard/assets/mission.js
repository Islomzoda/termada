// Mission Control stays in its own asset so the terminal runtime remains small
// and the mission view can evolve without destabilizing stream handling.
(() => {
  Object.assign(COPY.en,{missions:'Missions',missionControl:'Mission Control',loading:'Loading…',waitingFirstMission:'Waiting for the first mission',loadingMission:'Loading mission…',plannedMission:'Planned',interrupted:'Interrupted',succeeded:'Succeeded',cancelled:'Cancelled',passed:'Passed',skipped:'Skipped',verifiedByRuntime:'Verified by runtime',stepUpdated:'Plan step updated',missionCreated:'Mission created',missionResumed:'Mission resumed',missionCompleted:'Mission completed',commandStarted:'Command started',commandFinished:'Command finished',approvalRequested:'Approval requested',approvalResolved:'Approval resolved',policyDenied:'Policy denied',sessionReset:'Session reconnected',humanInput:'Human input',operatorControl:'Operator control',target:'Target',agent:'Agent',progress:'Progress',plan:'Plan',outcome:'Outcome',evidenceTimeline:'Evidence timeline',waitingRuntimeEvidence:'Waiting for runtime evidence',approvalRequired:'Approval required',approvalShellReview:'The command uses a shell and needs manual review',approvalRuleReview:'The command matched a required-approval rule',allowOnce:'Allow once',deny:'Deny',approved:'Approved',downloadEvidence:'Download evidence report',openEvidenceJob:'Open evidence job',controlPlaneUnavailable:'Control plane unavailable',dataUnavailable:'Operational data could not be loaded.',showingLastKnown:'Reconnecting · showing last known state',retry:'Retry',missionLoadFailed:'Mission could not be loaded',reportDownloadFailed:'Evidence report could not be downloaded'});
  let summaries = [], activeID = '', detail = null, pending = [], online = true, loadGeneration = 0, mode = true;

  function statusKey(status){
    return {planned:'plannedMission',running:'running',needs_attention:'needsApproval',interrupted:'interrupted',succeeded:'succeeded',failed:'failed',cancelled:'cancelled'}[status]||status;
  }
  function statusTone(status){
    return {planned:'idle',running:'running',needs_attention:'needs',interrupted:'needs',succeeded:'done',failed:'failed',cancelled:'failed'}[status]||'idle';
  }
  function isoTime(value){const stamp=Date.parse(value||'');return Number.isFinite(stamp)?stamp/1000:0;}
  function stepGlyph(status){return status==='passed'?'✓':status==='failed'?'!':status==='skipped'?'–':status==='running'?'›':'·';}
  function eventLabel(type){
    return {'mission.created':tr('missionCreated'),'mission.resumed':tr('missionResumed'),'mission.interrupted':tr('interrupted'),'mission.step_updated':tr('stepUpdated'),'mission.completed':tr('missionCompleted'),'job.start_requested':tr('request'),'job.started':tr('commandStarted'),'job.finished':tr('commandFinished'),'confirm.requested':tr('approvalRequested'),'confirm.resolved':tr('approvalResolved'),'policy.denied':tr('policyDenied'),'session.reset':tr('sessionReset'),'human_input.authorized':tr('humanInput'),'job.hold':tr('operatorControl')}[type]||type;
  }
	function resultLabel(status){return {exited:'completed',passed:'passed',failed:'failed',succeeded:'succeeded',cancelled:'cancelled',running:'running',awaiting_confirmation:'needsApproval'}[status]?tr({exited:'completed',passed:'passed',failed:'failed',succeeded:'succeeded',cancelled:'cancelled',running:'running',awaiting_confirmation:'needsApproval'}[status]):status;}

  function renderList(){
    document.getElementById('mission-count').textContent=summaries.length;
    const container=document.getElementById('mission-list');
    if(!online&&!summaries.length){
      renderStableHTML(container,`<div class="mission-offline" role="status"><b>${esc(tr('controlPlaneUnavailable'))}</b><span>${esc(tr('dataUnavailable'))}</span><button data-action="mission-retry">${esc(tr('retry'))}</button></div>`);
      return;
    }
    const stale=!online?`<div class="mission-stale" role="status">${esc(tr('showingLastKnown'))}<button data-action="mission-retry" aria-label="${esc(tr('retry'))}" title="${esc(tr('retry'))}">↻</button></div>`:'';
    const rows=summaries.map(item=>{
      const selected=item.id===activeID,tone=statusTone(item.status),progress=(item.steps_passed||0)+'/'+(item.steps_total||0),route=(item.workspace?item.workspace+' · ':'')+targetName(item.target);
      return `<button class="mission-row${selected?' sel':''}" data-action="mission-open" data-id="${esc(item.id)}"${selected?' aria-current="page"':''}><span class="mission-state ${tone}" aria-hidden="true"></span><span class="mission-row-main"><strong>${esc(item.title)}</strong><span>${esc(route)}</span></span><span class="mission-progress">${esc(progress)}</span><span class="mission-row-foot"><span class="mission-status ${tone}">${esc(tr(statusKey(item.status)))}</span><time data-relative-time="${esc(isoTime(item.updated_at))}"></time></span></button>`;
    }).join('');
    renderStableHTML(container,stale+(rows||`<div class="empty">${esc(tr('waitingFirstMission'))}</div>`));
    updateRelativeTimes(container);
  }

  function showPanel(){
    document.getElementById('mission-panel').hidden=false;
    document.getElementById('workspace-shell').hidden=true;
    document.getElementById('workspace-head').hidden=true;
  }

  function renderDetail(){
    showPanel();
    const container=document.getElementById('mission-content');
    if(!activeID){container.className='mission-empty';container.innerHTML=`<div><h2>${esc(tr('missionControl'))}</h2><p>${esc(tr('waitingFirstMission'))}</p></div>`;return;}
    if(!detail||detail.id!==activeID){container.className='mission-empty';container.innerHTML=`<div class="mission-loading" role="status">${esc(tr('loadingMission'))}</div>`;return;}
    const tone=statusTone(detail.status),steps=detail.steps||[],events=(detail.events||[]).slice(-120),passed=steps.filter(step=>step.status==='passed'||step.status==='skipped').length;
    const plan=steps.map(step=>`<li class="mission-step ${esc(step.status)}"><span class="step-glyph" aria-hidden="true">${stepGlyph(step.status)}</span><span class="step-copy"><strong>${esc(step.title)}</strong><span>${esc(tr(step.status==='passed'?'verifiedByRuntime':step.status))}${step.note?' · '+esc(step.note):''}</span></span>${step.job_id?`<button class="step-job" data-action="open-job" data-id="${esc(step.job_id)}" title="${esc(tr('openEvidenceJob'))}">${esc(short(step.job_id,10))}</button>`:''}</li>`).join('');
    const timeline=events.map(event=>renderEvent(event)).join('');
	const approval=pending.find(item=>item.session_id===detail.session_id),approvalBand=approval?`<section class="mission-gate" aria-label="${esc(tr('approvalRequired'))}"><div><strong>${esc(tr('approvalRequired'))}</strong><span>${esc(approvalReason(approval.reason||''))}${approval.matched?' · '+esc(approval.matched):''}</span></div><code>${esc(displayCommand(approval.command))}</code><div class="mission-gate-actions"><button class="primary" data-action="resolve" data-kind="approve" data-id="${esc(approval.confirmation_id)}">${esc(tr('allowOnce'))}</button><button class="danger" data-action="resolve" data-kind="deny" data-id="${esc(approval.confirmation_id)}">${esc(tr('deny'))}</button></div></section>`:'';
    const stale=!online?`<div class="mission-stale detail-stale" role="status">${esc(tr('showingLastKnown'))}<button data-action="mission-retry">${esc(tr('retry'))}</button></div>`:'';
    container.className='mission-detail';
    container.innerHTML=`${stale}<header class="mission-head"><div class="mission-title"><span class="mission-kicker">${esc(tr('missionControl'))}</span><h2>${esc(detail.title)}</h2><p>${esc(detail.goal)}</p></div><div class="mission-actions"><span class="mission-status ${tone}">${esc(tr(statusKey(detail.status)))}</span><button class="icon-button" data-action="mission-download" data-id="${esc(detail.id)}" aria-label="${esc(tr('downloadEvidence'))}" title="${esc(tr('downloadEvidence'))}">↓</button></div><dl class="mission-meta"><div><dt>${esc(tr('target'))}</dt><dd>${esc(targetName(detail.target))}</dd></div><div><dt>${esc(tr('agent'))}</dt><dd>${esc(detail.owner||tr('operator'))}</dd></div><div><dt>${esc(tr('session'))}</dt><dd class="mono">${esc(short(detail.session_id,16))}</dd></div><div><dt>${esc(tr('progress'))}</dt><dd>${passed}/${steps.length}</dd></div></dl></header>${approvalBand}<div class="mission-body"><section class="mission-plan"><div class="mission-section-title"><h3>${esc(tr('plan'))}</h3><span>${passed}/${steps.length}</span></div><ol>${plan}</ol>${detail.summary?`<section class="mission-outcome"><h3>${esc(tr('outcome'))}</h3><p>${esc(detail.summary)}</p></section>`:''}</section><section class="mission-timeline" aria-label="${esc(tr('evidenceTimeline'))}"><div class="mission-section-title"><h3>${esc(tr('evidenceTimeline'))}</h3><span>${events.length} ${esc(tr('events'))}</span></div><div class="mission-events">${timeline||`<div class="empty">${esc(tr('waitingRuntimeEvidence'))}</div>`}</div></section></div>`;
  }

  function renderEvent(event){
    let result=resultLabel(event.status||''),message=event.type==='job.finished'&&event.message===event.status?'':event.message;
    if(event.exit_code!=null)result+=(result?', ':'')+'exit '+event.exit_code;
    if(event.approved!=null)result=event.approved?tr('approved'):tr('deniedLabel');
    return `<article class="mission-event ${esc(statusTone(event.type==='confirm.requested'?'needs_attention':event.status))}"><time>${esc(new Date(event.time).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'}))}</time><span class="event-dot" aria-hidden="true"></span><div class="event-copy"><div><strong>${esc(eventLabel(event.type))}</strong>${event.sequence?`<span class="mono">#${esc(event.sequence)}</span>`:''}</div>${message?`<p class="mono">${esc(message)}</p>`:''}${result?`<span class="event-result">${esc(result)}</span>`:''}</div></article>`;
  }

  async function loadDetail(id){
    const generation=++loadGeneration;
    try{
      const result=await api('/api/mission/get?id='+encodeURIComponent(id));
      if(generation!==loadGeneration||id!==activeID)return;
      if(result.error)throw new Error(errorText(result,tr('missionLoadFailed')));
      detail=result;online=true;renderDetail();renderList();
    }catch(error){
      if(generation!==loadGeneration||id!==activeID)return;
      online=false;renderList();
      if(!detail){const container=document.getElementById('mission-content');container.className='mission-empty';container.innerHTML=`<div class="mission-offline" role="status"><b>${esc(tr('missionLoadFailed'))}</b><button data-action="mission-retry">${esc(tr('retry'))}</button></div>`;}
      else renderDetail();
    }
  }

  function open(id){
    if(!id)return;
	mode=true;
    activeID=id;detail=detail&&detail.id===id?detail:null;
    Object.keys(terms).forEach(disposeTerm);activeConversationKey='';
    showPanel();renderList();renderDetail();void loadDetail(id);setSidebarOpen(false,'');
    setTimeout(()=>document.getElementById('mission-panel').focus(),0);
  }

  function closeDetail(){
	mode=false;
	if(!activeID&&document.getElementById('mission-panel').hidden)return;
    activeID='';detail=null;loadGeneration++;
    document.getElementById('mission-panel').hidden=true;
    document.getElementById('workspace-shell').hidden=false;
    renderList();
  }

  async function download(id){
    const response=await fetch('/api/mission/report?id='+encodeURIComponent(id)+'&download=1',{headers:authHeaders()});
    if(!response.ok)throw new Error(tr('reportDownloadFailed'));
    const blob=await response.blob(),href=URL.createObjectURL(blob),link=document.createElement('a');
    link.href=href;link.download=id+'-evidence.md';document.body.appendChild(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(href),1000);
  }

  function receiveState(items, approvals){
    summaries=Array.isArray(items)?items:[];pending=Array.isArray(approvals)?approvals:[];online=true;renderList();
    if(activeID){if(!summaries.some(item=>item.id===activeID)){activeID='';detail=null;}else void loadDetail(activeID);}
	if(!activeID&&mode&&summaries.length)open(summaries[0].id);else if(mode)renderDetail();
  }

  function setOffline(){online=false;renderList();if(activeID)renderDetail();}
  function localize(){renderList();renderDetail();}
	function isActive(){return mode;}
	function activateMode(){mode=true;showPanel();if(!activeID&&summaries.length)open(summaries[0].id);else renderDetail();}

  document.addEventListener('click',event=>{
    const control=event.target.closest&&event.target.closest('[data-action]');if(!control)return;
    if(control.dataset.action==='mission-open')open(control.dataset.id||'');
    if(control.dataset.action==='mission-download')runAction(download(control.dataset.id||''),tr('reportDownloadFailed'));
    if(control.dataset.action==='mission-retry')void refresh();
  });

  window.MissionControl={receiveState,setOffline,localize,isActive,activateMode,closeDetail,open};
  applyCopy();
  renderList();renderDetail();
})();
