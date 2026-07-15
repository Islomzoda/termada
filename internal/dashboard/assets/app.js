// Token: taken from ?token= on first visit, kept only for this browser tab, and
// stripped from the address bar so it doesn't linger in history. If there's no
// token we show a clear gate instead of a silently-empty dashboard.
let token = new URLSearchParams(location.search).get('token') || '';
if(token){
  try{ sessionStorage.setItem('termada_token', token); }catch(_){}
  try{ history.replaceState(null,'',location.pathname); }catch(_){}
}else{
  try{ token = sessionStorage.getItem('termada_token') || ''; }catch(_){}
}
function authHeaders(){ return token ? {'Authorization':'Bearer '+token} : {}; }
function withToken(path){
  if(!token) return path;
  return path+(path.includes('?')?'&':'?')+'token='+encodeURIComponent(token);
}
const FitAddon = window.FitAddon ? window.FitAddon.FitAddon : null;
const COPY={
  en:{workspaces:'workspaces',workspacesTitle:'Workspaces',active:'active',needYou:'need you',history:'History',stopAll:'Stop all',connections:'Connections',policies:'Policies',filterWorkspaces:'Filter by agent, workspace, target, or command',needsAttention:'Needs attention',connected:'connected',connecting:'connecting…',reconnecting:'reconnecting…',offline:'offline',locked:'locked',defaultWorkspace:'Default workspace',operator:'Operator',local:'Local',unknownAgent:'Unknown agent',unknownTarget:'Unknown target',all:'All',failed:'Failed',finished:'Finished',running:'Running',needsInput:'Needs input',needsApproval:'Needs approval',background:'Background',completed:'Completed',stopped:'Stopped',timedOut:'Timed out',unavailable:'Result unavailable',pending:'Pending',run:'run',runs:'runs',runsTitle:'Runs',loaded:'loaded',output:'Output',terminal:'Terminal',request:'Request',session:'Session',agentRequest:'Agent request',agentSession:'Agent session',runWith:'Run with',on:'on',liveSessionFrom:'Live session output from',terminalResponse:'Terminal response',responseContinued:'Response continued',waitingOutput:'Waiting for output…',waitingMore:'Waiting for more output…',sessionActive:'Session active',sessionClosed:'Session closed',liveTerminal:'Live terminal stream',streamEnded:'Stream ended',live:'live',archived:'archived',answered:'answered',sendingAnswer:'sending answer…',answerNotDelivered:'answer not delivered',noRuns:'No runs yet',noWorkspaces:'No workspaces yet',noMatchingWorkspaces:'No matching workspaces',selectWorkspace:'Select a workspace',waitingFirstRun:'Waiting for the first run',selectRun:'Select a run',now:'now'},
  ru:{workspaces:'пространства',workspacesTitle:'Пространства',active:'активно',needYou:'ждут вас',history:'История',stopAll:'Остановить всё',connections:'Подключения',policies:'Политики',filterWorkspaces:'Агент, проект, цель или команда',needsAttention:'Требует внимания',connected:'подключено',connecting:'подключение…',reconnecting:'переподключение…',offline:'нет связи',locked:'заблокировано',defaultWorkspace:'Основное пространство',operator:'Оператор',local:'Этот компьютер',unknownAgent:'Неизвестный агент',unknownTarget:'Неизвестная цель',all:'Все',failed:'Ошибки',finished:'Завершено',running:'Выполняется',needsInput:'Нужен ответ',needsApproval:'Нужно подтверждение',background:'В фоне',completed:'Готово',stopped:'Остановлено',timedOut:'Превышено время',unavailable:'Результат недоступен',pending:'Ожидание',run:'запуск',runs:'запусков',runsTitle:'Запуски',loaded:'загружено',output:'Вывод',terminal:'Терминал',request:'Запрос',session:'Сессия',agentRequest:'Запрос агента',agentSession:'Сессия агента',runWith:'Запуск через',on:'на',liveSessionFrom:'Вывод сессии с',terminalResponse:'Ответ терминала',responseContinued:'Продолжение ответа',waitingOutput:'Ожидание вывода…',waitingMore:'Ожидание продолжения…',sessionActive:'Сессия активна',sessionClosed:'Сессия закрыта',liveTerminal:'Поток терминала',streamEnded:'Поток завершён',live:'выполняется',archived:'архив',answered:'ответ отправлен',sendingAnswer:'отправка ответа…',answerNotDelivered:'ответ не доставлен',noRuns:'Запусков пока нет',noWorkspaces:'Пространств пока нет',noMatchingWorkspaces:'Ничего не найдено',selectWorkspace:'Выберите пространство',waitingFirstRun:'Ожидание первого запуска',selectRun:'Выберите запуск',now:'сейчас'}
};
Object.assign(COPY.en,{error:'Error',inputNeeded:'Input needed',answerNotSent:'Answer not sent',programWaiting:'The program is waiting for your answer',yes:'Yes',no:'No',send:'Send',sendSecret:'Send secret',secretAnswer:'Secret answer',enterAnswer:'Enter an answer',blockInput:'Block agent input',pauseOutput:'Pause agent output',stop:'Stop'});
Object.assign(COPY.ru,{error:'Ошибка',inputNeeded:'Нужен ответ',answerNotSent:'Ответ не отправлен',programWaiting:'Программа ожидает ваш ответ',yes:'Да',no:'Нет',send:'Отправить',sendSecret:'Отправить секрет',secretAnswer:'Секретный ответ',enterAnswer:'Введите ответ',blockInput:'Блокировать ввод агента',pauseOutput:'Приостановить вывод агента',stop:'Остановить'});
Object.assign(COPY.en,{activityHistory:'Activity history',filterActivity:'Agent, command, or event',attention:'Attention',system:'System',activities:'activities',events:'events',noActivity:'No matching activity',verifyLog:'Verify log integrity with',execution:'Execution',connection:'Connection',systemActivity:'System activity',olderEventsHidden:'older events hidden'});
Object.assign(COPY.ru,{activityHistory:'История активности',filterActivity:'Агент, команда или событие',attention:'Внимание',system:'Система',activities:'действий',events:'событий',noActivity:'Подходящих действий нет',verifyLog:'Проверить целостность журнала:',execution:'Запуск',connection:'Подключение',systemActivity:'Системное событие',olderEventsHidden:'старых событий скрыто'});
Object.assign(COPY.en,{serversTitle:'Servers',connectedAgents:'Connected agents',noServers:'No servers configured',noConnections:'No connections',test:'Test',online:'online',idle:'idle',jobsLabel:'jobs',sessionsLabel:'sessions',deniedLabel:'denied',statusOk:'available',statusTesting:'testing',statusUnreachable:'unreachable'});
Object.assign(COPY.ru,{serversTitle:'Серверы',connectedAgents:'Подключённые агенты',noServers:'Серверы не настроены',noConnections:'Нет подключений',test:'Проверить',online:'онлайн',idle:'неактивен',jobsLabel:'запусков',sessionsLabel:'сессий',deniedLabel:'отклонено',statusOk:'доступен',statusTesting:'проверка',statusUnreachable:'недоступен'});
let locale='en';try{locale=sessionStorage.getItem('termada_locale')||((navigator.language||'').toLowerCase().startsWith('ru')?'ru':'en');}catch(_){}
function tr(key){return(COPY[locale]&&COPY[locale][key])||COPY.en[key]||key;}
function applyLocale(){document.documentElement.lang=locale;document.querySelectorAll('[data-i18n]').forEach(el=>{el.textContent=tr(el.dataset.i18n);});document.querySelectorAll('[data-i18n-placeholder]').forEach(el=>{el.placeholder=tr(el.dataset.i18nPlaceholder);});document.querySelectorAll('[data-stream-state]').forEach(el=>{el.textContent=tr(el.dataset.streamState);});const button=document.querySelector('.locale-button');if(button)button.textContent=locale==='en'?'RU':'EN';const conn=document.getElementById('conn');if(conn)conn.textContent=tr(conn.dataset.connectionState||'connecting');}
function setConnectionState(state){const conn=document.getElementById('conn');if(!conn)return;conn.dataset.connectionState=state;conn.textContent=tr(state);}
function toggleLocale(){locale=locale==='en'?'ru':'en';try{sessionStorage.setItem('termada_locale',locale);}catch(_){}applyLocale();Object.values(terms).forEach(localizeTerm);refreshDialogTree();renderAttention(lastPending,Object.values(lastJobs));renderWorkspace();if(auditRecs.length)renderAudit();scheduleStateRefresh(0);}

async function api(path, opts={}){
  opts.headers = Object.assign({'Content-Type':'application/json'}, authHeaders(), opts.headers||{});
  const r = await fetch(path, opts);
  if(r.status===401){ showGate(); throw new Error('unauthorized'); }
  const data=await r.json().catch(()=>({}));
  if(!r.ok&&!data.error) data.error={code:'http_'+r.status,message:'HTTP '+r.status};
  if(data&&typeof data==='object') Object.defineProperty(data,'_status',{value:r.status,enumerable:false});
  return data;
}
// The complete TCP API is token-gated; static assets remain readable so this
// prompt can collect the token without exposing operational data.
let gated=false;
function showGate(){gated=true;const g=document.getElementById('gate');if(g)g.style.display='flex';if(feedES){try{feedES.close();}catch(_){}feedES=null;}for(const key in terms){if(terms[key].es){try{terms[key].es.close();}catch(_){}terms[key].es=null;}}}
function submitGate(){
  const t=(document.getElementById('gate-token').value||'').trim();
  if(!t){ return; }
  token=t; gated=false;
  try{ sessionStorage.setItem('termada_token', t); }catch(_){}
	  const g=document.getElementById('gate'); if(g) g.style.display='none';
	  for(const key in terms) connectTermStream(terms[key]);
	  refresh().then(()=>startFeed()).catch(()=>{});
	  loadPolicies();
}
function esc(s){return (s==null?'':String(s)).replace(/[&<>"']/g,c=>({
  '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'
}[c]));}
function shellArg(v){v=String(v==null?'':v);return /^[a-zA-Z0-9_@%+=:,./-]+$/.test(v)?v:"'"+v.replace(/'/g,"'\\''")+"'";}
function cmd(c){return Array.isArray(c)?c.map(shellArg).join(' '):(c||'');}
function displayCommand(c){if(Array.isArray(c)&&c.length>=3&&/^(?:bash|sh|zsh)$/.test(c[0])&&/^-?[lc]+$/.test(c[1]))return c.slice(2).join(' ');return cmd(c);}
function commandRunner(c){return Array.isArray(c)&&c.length>=2&&/^(?:bash|sh|zsh)$/.test(c[0])&&/^-?[lc]+$/.test(c[1])?c[0]+' '+c[1]:'';}
function approvalReason(reason){if(!reason)return'';if(/ambiguous shell or wrapper/i.test(reason))return'The command uses a shell and needs manual review';if(/matched confirm rule/i.test(reason))return'The command matched a required-approval rule';return reason;}
function short(s,n){s=s||'';return s.length>n?s.slice(0,n)+'…':s;}
function relTime(unix){if(!unix)return'';const d=Math.max(0,Math.floor(Date.now()/1000-unix));if(d<5)return tr('now');if(locale==='ru'){if(d<60)return d+' сек назад';if(d<3600)return Math.floor(d/60)+' мин назад';if(d<86400)return Math.floor(d/3600)+' ч назад';return Math.floor(d/86400)+' дн назад';}if(d<60)return d+'s ago';if(d<3600)return Math.floor(d/60)+'m ago';if(d<86400)return Math.floor(d/3600)+'h ago';return Math.floor(d/86400)+'d ago';}
function timeSec(obj,name='created'){return obj&&obj[name+'_unix_ms']?obj[name+'_unix_ms']/1000:(obj&&obj[name+'_unix'])||0;}
function timeMS(obj,name='created'){return obj&&obj[name+'_unix_ms']||((obj&&obj[name+'_unix'])||0)*1000;}
function targetName(target){return !target||target==='local'?tr('local'):target;}
function duration(ms){if(!ms)return'';if(ms<1000)return ms+'ms';if(ms<60000)return(Math.round(ms/100)/10)+'s';return(Math.round(ms/6000)/10)+'m';}
function initials(name){const p=String(name||'AI').replace(/[^a-zA-Zа-яА-Я0-9]+/g,' ').trim().split(/\s+/);return(p.length>1?p[0][0]+p[1][0]:String(name||'AI').slice(0,2)).toUpperCase();}
function errorText(r,fallback){return r&&r.error?(r.error.message||r.error.code||fallback):fallback;}
function serverStatusLabel(status){return tr(status==='ok'?'statusOk':status==='testing'?'statusTesting':status==='unreachable'?'statusUnreachable':status||'statusUnreachable');}
function showToast(message,kind='ok'){const el=document.getElementById('toast');el.textContent=message;el.className='visible '+kind;clearTimeout(showToast.timer);showToast.timer=setTimeout(()=>{el.className='';},4500);}
function runAction(promise,fallback){void Promise.resolve(promise).catch(err=>showToast((err&&err.message)||fallback,'error'));}
const TERMINAL = ['exited','killed','failed','timed_out','orphaned'];
const STATUS_KEYS={running:'running',awaiting_input:'needsInput',awaiting_confirmation:'needsApproval',backgrounded:'background',exited:'completed',killed:'stopped',failed:'failed',timed_out:'timedOut',orphaned:'unavailable'};
function statusLabel(j){if(!j)return tr('pending');if(j.status==='exited'&&j.exit_code!=null&&j.exit_code!==0)return tr('failed')+' · exit '+j.exit_code;return STATUS_KEYS[j.status]?tr(STATUS_KEYS[j.status]):j.status||tr('pending');}
function statusClass(j){if(!j)return'running';if(j.status==='exited'&&j.exit_code!=null&&j.exit_code!==0)return'failed';return j.status||'running';}
function jobFailed(j){return!!j&&(['failed','killed','timed_out','orphaned'].includes(j.status)||(j.status==='exited'&&j.exit_code!=null&&j.exit_code!==0));}
function runWord(count){if(locale!=='ru')return count===1?tr('run'):tr('runs');const n=Math.abs(count)%100,n1=n%10;return n>10&&n<20?'запусков':n1===1?'запуск':n1>=2&&n1<=4?'запуска':'запусков';}
function runCountLabel(count){return count+' '+runWord(count);}
function failureWord(count){if(locale!=='ru')return tr('failed').toLowerCase();const n=Math.abs(count)%100,n1=n%10;return n>10&&n<20?'ошибок':n1===1?'ошибка':n1>=2&&n1<=4?'ошибки':'ошибок';}
function secretPrompt(prompt){return /pass(word|phrase)|парол|секрет|secret|token|токен|private\s*key|ключ/i.test(prompt||'');}
function binaryPrompt(prompt){return /\[[^\]]*(?:y\s*\/\s*n|yes\s*\/\s*no|д\s*\/\s*н)[^\]]*\]|\((?:y\s*\/\s*n|yes\s*\/\s*no|да\s*\/\s*нет)\)|continue\?|proceed\?|allow\?|разрешить\?|продолжить\?/i.test(prompt||'');}

// ---- live conversation + advanced terminal -------------------------------
const terms = {};
let active = null;
const mobileSidebarQuery=window.matchMedia('(max-width:780px)');
const UNKNOWN_TARGET='__unknown__',OPERATOR_OWNER='__operator__';
let sidebarOpen=false,sidebarView='conversations',conversationFilter='',lastFocusedControl=null,activeConversationKey='',executionFilter='all',lastConversationGroups=[],lastJobsOmitted=0;
let stateRevision=0,activeJobsCount=0,fallbackTimer=null,stateRefreshTimer=null;

function fitActiveTerm(){const info=active&&terms[active];if(info&&info.fit)setTimeout(()=>{try{info.fit.fit();}catch(_){}} ,0);}
function setSidebarOpen(open,focusTarget=''){
  const mobile=mobileSidebarQuery.matches,layout=document.getElementById('layout'),sidebar=document.getElementById('sidebar'),toggle=document.getElementById('sidebar-toggle'),backdrop=document.getElementById('sidebar-backdrop'),main=document.querySelector('main'),header=document.querySelector('header');
  sidebarOpen=mobile&&!!open;layout.classList.toggle('sidebar-open',sidebarOpen);toggle.setAttribute('aria-expanded',String(sidebarOpen));toggle.setAttribute('aria-label',sidebarOpen?'Close navigation':'Open navigation');toggle.title=sidebarOpen?'Close navigation':'Open navigation';backdrop.hidden=!sidebarOpen;backdrop.setAttribute('aria-hidden',String(!sidebarOpen));
  if(mobile){sidebar.inert=!sidebarOpen;sidebar.setAttribute('aria-hidden',String(!sidebarOpen));main.inert=sidebarOpen;Array.from(header.children).forEach(child=>{if(child!==toggle)child.inert=sidebarOpen;});}else{sidebar.inert=false;sidebar.removeAttribute('aria-hidden');main.inert=false;Array.from(header.children).forEach(child=>child.inert=false);}
  if(sidebarOpen){setTimeout(()=>{if(!sidebarOpen)return;const selected=sidebar.querySelector('.side-tab[aria-selected="true"]');if(selected)selected.focus();},220);}
  else if(focusTarget==='main'){setTimeout(()=>{const info=active&&terms[active],target=info&&info.host.querySelector('.transcript');if(target)target.focus();},0);}
  else if(focusTarget==='toggle'&&mobile){setTimeout(()=>toggle.focus(),0);}
  else if(focusTarget==='sidebar'){const selected=sidebar.querySelector('.side-tab[aria-selected="true"]');if(selected)selected.focus();}
  setTimeout(fitActiveTerm,220);
}
function syncSidebarMode(event){const toggle=document.getElementById('sidebar-toggle'),focusTarget=event&&event.matches?'toggle':event&&lastFocusedControl===toggle?'sidebar':'';setSidebarOpen(false,focusTarget);}
function setSidebarView(view,focusTab=false){
  if(!['conversations','connections','policies'].includes(view))return;sidebarView=view;
  document.querySelectorAll('.side-tab').forEach(tab=>{const selected=tab.dataset.view===view;tab.setAttribute('aria-selected',String(selected));tab.tabIndex=selected?0:-1;});
  document.querySelectorAll('.side-panel').forEach(panel=>{panel.hidden=panel.dataset.sidebarView!==view;});
  if(focusTab){const tab=document.querySelector('.side-tab[data-view="'+view+'"]');if(tab)tab.focus();}
  fitActiveTerm();
}
function renderFocusKey(node){if(!node)return null;return{tag:node.tagName,action:node.dataset.action||'',id:node.dataset.id||'',name:node.dataset.name||'',view:node.dataset.view||'',kind:node.dataset.kind||'',key:node.dataset.key||''};}
function renderFocusMatch(node,key){return node.tagName===key.tag&&(node.dataset.action||'')===key.action&&(node.dataset.id||'')===key.id&&(node.dataset.name||'')===key.name&&(node.dataset.view||'')===key.view&&(node.dataset.kind||'')===key.kind&&(node.dataset.key||'')===key.key;}
function renderStableHTML(element,html){
  if(!element||element._renderedHTML===html)return false;
  const focused=document.activeElement&&element.contains(document.activeElement)?renderFocusKey(document.activeElement):null,scroller=element.closest('.side-scroll'),scrollTop=scroller&&scroller.scrollTop;
  element.innerHTML=html;element._renderedHTML=html;
  if(scroller)scroller.scrollTop=scrollTop;
  if(focused){const replacement=Array.from(element.querySelectorAll(focused.tag.toLowerCase())).find(node=>renderFocusMatch(node,focused));if(replacement&&!replacement.disabled)replacement.focus({preventScroll:true});}
  return true;
}
function updateRelativeTimes(root){(root||document).querySelectorAll('[data-relative-time]').forEach(el=>{const at=Number(el.dataset.relativeTime||0);el.textContent=at?relTime(at):'';});}
function syncSidebarSelection(){
  document.querySelectorAll('#sessions .conversation-open').forEach(node=>{const selected=node.dataset.conversationKey===activeConversationKey;node.classList.toggle('sel',selected);if(selected)node.setAttribute('aria-current','page');else node.removeAttribute('aria-current');});
  const activeJobID=active&&active.startsWith('job:')?active.slice(4):'';
  document.querySelectorAll('#execution-list .execution-row').forEach(node=>{const selected=node.dataset.id===activeJobID;node.classList.toggle('sel',selected);if(selected)node.setAttribute('aria-current','true');else node.removeAttribute('aria-current');});
  document.querySelectorAll('#sessions .dialog-group').forEach(group=>group.classList.toggle('has-selection',!!group.querySelector('[aria-current="page"]')));
}

function canonicalOwner(owner){return owner||OPERATOR_OWNER;}
function displayOwner(owner){return owner===OPERATOR_OWNER?tr('operator'):owner||tr('unknownAgent');}
function conversationKey(owner,target,workspace){return JSON.stringify([canonicalOwner(owner),target||UNKNOWN_TARGET,workspace||'']);}
function displayTarget(target){return target===UNKNOWN_TARGET?tr('unknownTarget'):targetName(target);}
function displayWorkspace(workspace){return workspace||tr('defaultWorkspace');}
function routeForSession(session){return{owner:canonicalOwner(session&&session.owner),target:(session&&session.target)||'local',workspace:(session&&session.workspace)||''};}
function routeForJob(job){const session=job&&lastSessions[job.session_id];if(session)return routeForSession(session);return{owner:canonicalOwner(job&&job.owner),target:(job&&job.target)||UNKNOWN_TARGET,workspace:(job&&job.workspace)||''};}
function termConversationKey(info){if(!info)return'';const route=info.job?routeForJob(info.job):routeForSession(info.session);return conversationKey(route.owner,route.target,route.workspace);}
function conversationGroup(key){return lastConversationGroups.find(group=>group.key===key)||null;}
function executionCategory(job){if(!TERMINAL.includes(job.status))return'active';return jobFailed(job)?'failed':'finished';}
function buildConversationGroups(sessions,jobs){
	  const groups=new Map(),sessionByID={};
	  const ensure=(owner,target,workspace)=>{const key=conversationKey(owner,target,workspace);let group=groups.get(key);if(!group){group={key,owner,target,workspace,sessions:[],jobs:[],sessionIDs:new Set(),jobIDs:new Set()};groups.set(key,group);}return group;};
	  const addSession=session=>{if(!session||!session.session_id)return;sessionByID[session.session_id]=session;const route=routeForSession(session),group=ensure(route.owner,route.target,route.workspace);if(!group.sessionIDs.has(session.session_id)){group.sessionIDs.add(session.session_id);group.sessions.push(session);}};
	  const addJob=job=>{if(!job||!job.job_id||job.status==='awaiting_confirmation')return;const session=sessionByID[job.session_id]||lastSessions[job.session_id],route=session?routeForSession(session):routeForJob(job),group=ensure(route.owner,route.target,route.workspace);if(!group.jobIDs.has(job.job_id)){group.jobIDs.add(job.job_id);group.jobs.push(job);}};
  sessions.forEach(addSession);jobs.forEach(addJob);
  Object.values(terms).forEach(info=>{if(info.session&&!sessionByID[info.session.session_id])addSession(info.session);if(info.job)addJob(info.job);});
  const rows=Array.from(groups.values());rows.forEach(group=>{group.sessions.sort((a,b)=>timeMS(b)-timeMS(a));group.jobs.sort((a,b)=>timeMS(b)-timeMS(a));group.activity=Math.max(0,...group.sessions.map(item=>timeMS(item)),...group.jobs.map(item=>timeMS(item)));});rows.sort((a,b)=>b.activity-a.activity||a.key.localeCompare(b.key));return rows;
}
function renderWorkspace(){
  const group=conversationGroup(activeConversationKey),head=document.getElementById('workspace-head'),panel=document.getElementById('execution-panel'),shell=document.getElementById('workspace-shell'),nudge=document.getElementById('nudge');
  head.hidden=!group;panel.hidden=!group;shell.classList.toggle('empty',!group);
	  if(!group){renderStableHTML(document.getElementById('execution-list'),'');nudge.querySelector('.nudge-title').textContent=tr('selectWorkspace');nudge.style.display=active?'none':'flex';return;}
	  const activeJobs=group.jobs.filter(job=>executionCategory(job)==='active'),failedJobs=group.jobs.filter(job=>executionCategory(job)==='failed');
	  document.getElementById('workspace-route').textContent=displayWorkspace(group.workspace);
	  document.getElementById('workspace-summary').textContent=displayOwner(group.owner)+' → '+displayTarget(group.target)+' · '+runCountLabel(group.jobs.length)+' · '+activeJobs.length+' '+tr('active')+(failedJobs.length?' · '+failedJobs.length+' '+failureWord(failedJobs.length):'');
	  document.getElementById('execution-count').textContent=tr('loaded')+': '+group.jobs.length;
	  document.querySelectorAll('.execution-filter').forEach(button=>{const selected=button.dataset.filter===executionFilter;button.setAttribute('aria-pressed',String(selected));button.textContent=(button.dataset.filter==='all'?tr('all')+' ':button.dataset.filter==='active'?tr('active')+' ':tr('failed')+' ')+(button.dataset.filter==='all'?group.jobs.length:button.dataset.filter==='active'?activeJobs.length:failedJobs.length);});
  const visible=executionFilter==='all'?group.jobs:group.jobs.filter(job=>executionCategory(job)===executionFilter),activeJobID=active&&active.startsWith('job:')?active.slice(4):'';
	  const html=visible.length?visible.map(job=>{const label=statusLabel(job),state=statusClass(job);return `<button class="execution-row${job.job_id===activeJobID?' sel':''}" data-action="open-job" data-id="${esc(job.job_id)}" title="${esc(displayCommand(job.command))}"${job.job_id===activeJobID?' aria-current="true"':''}><span class="execution-command">${esc(displayCommand(job.command))}</span><span class="execution-time" data-relative-time="${esc(timeSec(job))}"></span><span class="execution-meta"><span>${esc(short(job.job_id,12))}</span>${job.awaiting_input?'<span>'+esc(tr('needsInput'))+'</span>':''}<span class="badge st-${esc(state)}">${esc(label)}</span></span></button>`;}).join(''):`<div class="empty">${esc(tr('noRuns'))}</div>`;
  const list=document.getElementById('execution-list');renderStableHTML(list,html);updateRelativeTimes(list);
  const limit=document.getElementById('execution-limit');limit.hidden=!lastJobsOmitted;limit.textContent=lastJobsOmitted?'Recent history only · '+lastJobsOmitted+' older runs not loaded':'';
	  nudge.querySelector('.nudge-title').textContent=group.jobs.length?tr('selectRun'):tr('waitingFirstRun');nudge.style.display=active?'none':'flex';syncSidebarSelection();
}
function setExecutionFilter(filter){if(!['all','active','failed'].includes(filter))return;executionFilter=filter;renderWorkspace();}

function makeTerm(config){
  const {key,kind,job,session,controlsHTML,streamPath,onInput,promptInput}=config;
  if(terms[key]){ focusTab(key); return terms[key]; }
  Object.keys(terms).forEach(openKey=>disposeTerm(openKey));
	  const route=job?routeForJob(job):routeForSession(session),actor=displayOwner(route.owner),target=displayTarget(route.target),workspace=displayWorkspace(route.workspace);
  const command=job?displayCommand(job.command):'',runner=job?commandRunner(job.command):'';
  const canInput=kind==='session'||!!(job&&!TERMINAL.includes(job.status)&&job.stream_available!==false);
  const created=timeSec(job||session);
  const host=document.createElement('div'); host.className='termhost';
	  host.innerHTML=`<div class="termctl"><div class="grip"><div class="context-route">${esc(workspace)}</div>
	      <div class="context-sub">${esc(actor)} → ${esc(target)} · ${kind==='job'?esc(tr('request'))+' '+esc(short(job.job_id,12)):esc(tr('session'))+' '+esc(short(session.session_id,12))}${created?' · '+esc(relTime(created)):''}</div></div>
	    <div class="view-switch" aria-label="View mode"><button class="active" data-action="set-view" data-key="${esc(key)}" data-view="dialog" aria-pressed="true" data-i18n="output">${esc(tr('output'))}</button><button data-action="set-view" data-key="${esc(key)}" data-view="terminal" aria-pressed="false" data-i18n="terminal">${esc(tr('terminal'))}</button></div>
    <span class="takeover">${controlsHTML||''}</span></div>
    <div class="chatview"><div class="transcript" role="region" aria-label="Request timeline" tabindex="0">
	      <div class="turn request-turn"><div class="avatar">${esc(initials(actor))}</div><div><div class="msg-head"><b data-i18n="${kind==='job'?'agentRequest':'agentSession'}">${esc(tr(kind==='job'?'agentRequest':'agentSession'))}</b><span>${esc(actor)} → ${esc(target)}${created?' · '+esc(relTime(created)):''}</span></div>
	        <div class="message"><div class="request-text">${kind==='job'?esc(tr('runWith'))+(runner?' '+esc(runner):'')+' '+esc(tr('on'))+' '+esc(target):esc(tr('liveSessionFrom'))+' '+esc(target)}</div>${command?'<pre class="request-command">'+esc(command)+'</pre>':''}</div></div></div>
	      <div class="turn response-turn"><div class="avatar terminal">&gt;_</div><div><div class="msg-head"><b data-i18n="terminalResponse">${esc(tr('terminalResponse'))}</b><span class="stream-state" data-stream-state="connecting">${esc(tr('connecting'))}</span></div>
	        <div class="message output-shell"><div class="output-gap">Earlier output is no longer available</div><pre class="chat-output output-empty" data-empty-key="waitingOutput" data-empty="${esc(tr('waitingOutput'))}">${esc(tr('waitingOutput'))}</pre>
	          ${job?`<div class="result-line"><span class="badge st-${esc(statusClass(job))}">${esc(statusLabel(job))}</span><strong class="result-detail">${job.duration_ms?esc(duration(job.duration_ms)):''}</strong><span class="result-reason">${job.reason?esc(job.reason):''}</span></div>`:`<div class="result-line"><span class="badge st-running">${esc(tr('sessionActive'))}</span><span class="result-reason">${esc(tr('liveTerminal'))}</span></div>`}</div></div></div>
    </div></div><div class="termnotice" role="status" aria-live="assertive"><div class="prompt-box"><div class="prompt-title"><span class="notice-label"></span><span class="prompt-heading"></span></div><div class="notice-text"></div><div class="prompt-actions"></div><div class="prompt-hint"></div></div></div>
    <div class="terminalview"><div class="termel"></div></div>`;
  document.getElementById('termwrap').appendChild(host);

	  const info={key,kind,job,session,term:null,fit:null,es:null,host,streamPath,onInput,promptInput:promptInput||onInput,canInput,view:'dialog',cursor:'',streamErrors:0,output:'',outputSystemKey:'',outputEl:host.querySelector('.chat-output'),chatParser:null,renderFrame:0,terminalReplay:'',hadCurrentOutput:false,inputQueue:Promise.resolve(),inputFailed:false,inputError:null,inputGeneration:0,rawBuffer:'',rawTimer:null,promptText:'',promptPreview:'',skipPromptPrefix:'',sendingPrompt:false};
	  resetChatParser(info);
	  terms[key]=info;if(job)updateResult(info,job);connectTermStream(info);focusTab(key);return info;
}
function ensureTerminal(info){
  if(!info||info.term)return info&&info.term;
  const term=new Terminal({convertEol:true,cursorBlink:true,fontSize:12.5,scrollback:4000,disableStdin:!info.canInput,fontFamily:'ui-monospace,SFMono-Regular,Menlo,monospace',theme:{background:'#000000',foreground:'#e6edf3',cursor:'#58a6ff'}});
  term.open(info.host.querySelector('.termel'));let fit=null;if(FitAddon){fit=new FitAddon();term.loadAddon(fit);}info.term=term;info.fit=fit;
  if(info.onInput)term.onData(data=>{if(info.canInput)void queueRawTermInput(info,info.onInput,data);});
  if(info.terminalReplay)term.write(info.terminalReplay);return term;
}
function writeTerminal(info,chunk){if(!info||!chunk)return;info.terminalReplay+=chunk;if(info.terminalReplay.length>524288)info.terminalReplay=info.terminalReplay.slice(-524288);if(info.term)info.term.write(chunk);}
function localizeTerm(info){
  if(!info||!info.host)return;const route=info.job?routeForJob(info.job):routeForSession(info.session),actor=displayOwner(route.owner),target=displayTarget(route.target),created=timeSec(info.job||info.session),kind=info.kind;
  const heading=info.host.querySelector('.context-route'),sub=info.host.querySelector('.context-sub');if(heading)heading.textContent=displayWorkspace(route.workspace);if(sub)sub.textContent=actor+' → '+target+' · '+tr(kind==='job'?'request':'session')+' '+short(kind==='job'?info.job.job_id:info.session.session_id,12)+(created?' · '+relTime(created):'');
  const request=info.host.querySelector('.request-turn'),avatar=request&&request.querySelector('.avatar'),meta=request&&request.querySelector('.msg-head span'),text=request&&request.querySelector('.request-text');if(avatar)avatar.textContent=initials(actor);if(meta)meta.textContent=actor+' → '+target+(created?' · '+relTime(created):'');if(text){const runner=kind==='job'?commandRunner(info.job.command):'';text.textContent=kind==='job'?tr('runWith')+(runner?' '+runner:'')+' '+tr('on')+' '+target:tr('liveSessionFrom')+' '+target;}
	  if(info.outputSystemKey){info.output=tr(info.outputSystemKey);renderChatOutput(info);}info.host.querySelectorAll('.chat-output').forEach(out=>{out.dataset.empty=tr(out.dataset.emptyKey||'waitingOutput');if(out.classList.contains('output-empty'))out.textContent=out.dataset.empty;});if(info.job)updateResult(info,info.job);else updateSessionResult(info,info.canInput);
}
function setTermNotice(info,kind,message){
  if(!info||!info.host) return;
  const notice=info.host.querySelector('.termnotice');
  if(!notice) return;
  const text=message||'';
  if(kind==='prompt'&&text){info.promptText=text;if(info.kind==='session'&&!info.promptJobID){const current=lastSessions[info.session.session_id];info.promptJobID=current&&current.current_job_id||'';}}
  else if(!text&&kind!=='error'){info.promptText='';info.promptJobID='';}
  const prompt=kind==='error'?info.promptText:text;
  if(kind==='prompt'&&prompt){info.promptPreview=prompt;info.hadAnyOutput=true;info.hadCurrentOutput=true;renderChatOutput(info);}
  else if(!text&&kind!=='error'){info.promptPreview='';renderChatOutput(info);}
  const noticeKey=text?(kind+'\n'+(info.promptJobID||'')+'\n'+text):'';
  if(notice.dataset.noticeKey===noticeKey) return;
  const wasVisible=notice.classList.contains('visible');
  notice.dataset.noticeKey=noticeKey;
  notice.className='termnotice'+(text?' visible '+kind:'');notice.dataset.kind=text?kind:'';
	  notice.querySelector('.notice-label').textContent=tr(kind==='error'?'error':'inputNeeded');
	  notice.querySelector('.prompt-heading').textContent=tr(kind==='error'?'answerNotSent':'programWaiting');
  notice.querySelector('.notice-text').textContent=kind==='error'?text:prompt;
  const actions=notice.querySelector('.prompt-actions'),hint=notice.querySelector('.prompt-hint');actions.innerHTML='';hint.textContent='';
  if(text&&prompt){
    if(binaryPrompt(prompt)){
	      actions.innerHTML=`<button class="primary" data-action="answer-prompt" data-key="${esc(info.key)}" data-answer="y">${esc(tr('yes'))} (Y)</button><button data-action="answer-prompt" data-key="${esc(info.key)}" data-answer="n">${esc(tr('no'))} (N)</button>`;
      hint.textContent='These buttons send Y or N to the program.';
    }else{
      const secret=secretPrompt(prompt);info.promptSecret=secret;
	      actions.innerHTML=`<input class="prompt-input" type="${secret?'password':'text'}" data-key="${esc(info.key)}" placeholder="${esc(tr(secret?'secretAnswer':'enterAnswer'))}" autocomplete="off" spellcheck="false"><button class="primary" data-action="send-prompt" data-key="${esc(info.key)}">${esc(tr(secret?'sendSecret':'send'))}</button>`;
      hint.textContent=secret?'The value is hidden and registered for output redaction.':'The answer is sent to the waiting program.';
    }
  }
  if(info.fit&&(wasVisible!==!!text||info.view==='terminal'))setTimeout(()=>{try{info.fit.fit();}catch(_){}} ,0);
}
function queueTermInput(info,onInput,data,options={}){
  if(info.inputFailed){
    info.inputFailed=false;info.inputError=null;info.inputGeneration++;info.inputQueue=Promise.resolve();
    setTermNotice(info,info.promptText?'prompt':'',info.promptText);
  }
  const generation=info.inputGeneration;
  const next=info.inputQueue.then(async()=>{
    if(generation!==info.inputGeneration||info.inputFailed)throw info.inputError||new Error('input discarded after an earlier delivery failure');
    const result=await onInput(data,options);
    if(result&&result.error) throw new Error(result.error.message||result.error.code||'input rejected');
    if(options.clearNotice&&/[\r\n]/.test(data)) setTermNotice(info,'','');
    return result;
  });
  info.inputQueue=next.then(()=>undefined,err=>{if(generation===info.inputGeneration&&!info.inputFailed){info.inputFailed=true;info.inputError=err;setTermNotice(info,'error','Could not send input: '+(err&&err.message?err.message:String(err)));}});
  return next;
}
function queueRawTermInput(info,onInput,data){
  info.rawBuffer+=data;
  clearTimeout(info.rawTimer);
  if(info.rawBuffer.length>=256||/[\x00-\x1f\x7f]/.test(data))return flushRawTermInput(info,onInput);
  info.rawTimer=setTimeout(()=>{void flushRawTermInput(info,onInput);},24);
  return Promise.resolve();
}
function flushRawTermInput(info,onInput=info.onInput){
  clearTimeout(info.rawTimer);info.rawTimer=null;
  const data=info.rawBuffer;info.rawBuffer='';
  if(!data||!onInput)return Promise.resolve();
  return queueTermInput(info,onInput,data,{appendNewline:false,secret:false,clearNotice:true}).catch(()=>{});
}
function archiveTermStream(info){
	  if(!info)return;if(!info.hadCurrentOutput){try{if(info.chatParser)info.chatParser.dispose();}catch(_){}info.chatParser=null;info.outputSystemKey='unavailable';info.output=tr('unavailable');info.hadAnyOutput=true;renderChatOutput(info);}setCurrentStreamState(info,'archived');setTermNotice(info,'','');setTermInputEnabled(info,false);if(info.job)updateResult(info,info.job);
}
function connectTermStream(info){
  if(info.es){ try{info.es.close();}catch(_){ } }
  if(info.job&&(info.job.stream_available===false||info.job.status==='orphaned')){
    archiveTermStream(info);
    return;
  }
  let path=info.streamPath;if(info.cursor) path+=(path.includes('?')?'&':'?')+'cursor='+encodeURIComponent(info.cursor);
  const es=new EventSource(withToken(path));
  info.es=es;
  es.onmessage=ev=>{ let m; try{m=JSON.parse(ev.data);}catch(_){return;}
    info.streamErrors=0;
    if(ev.lastEventId) info.cursor=ev.lastEventId;
    if(m.error){const streamError=typeof m.error==='string'?m.error:(m.error.message||m.error.code||JSON.stringify(m.error));if(info.kind==='job'&&/not[_ ]found/i.test(streamError)){archiveTermStream(info);es.close();if(info.es===es)info.es=null;return;}setTermNotice(info,'error','Stream error: '+streamError);setCurrentStreamState(info,'unavailable');es.close();if(info.es===es)info.es=null;return;}
    if(Object.prototype.hasOwnProperty.call(m,'job_id'))info.promptJobID=m.job_id||'';
    if(!info.inputFailed&&Object.prototype.hasOwnProperty.call(m,'awaiting_input')){
      setTermNotice(info,m.awaiting_input?'prompt':'',m.awaiting_input?(m.prompt||'Input is required'):'');
    }
	    if(m.gap){const gap=currentResponsePart(info,'.output-gap');if(gap)gap.classList.add('visible');writeTerminal(info,'\r\n\x1b[33m-- earlier output is no longer available --\x1b[0m\r\n');}
	    if(m.chunk){appendChatOutput(info,m.chunk);writeTerminal(info,m.chunk);}
	    setCurrentStreamState(info,m.done?'completed':'live');
    if(m.status) updateResult(info,Object.assign({},info.job||{},{status:m.status}));
	    if(m.done){setTermNotice(info,'','');if(!info.hadCurrentOutput)appendChatOutput(info,info.hadAnyOutput?'No additional output.':(info.kind==='session'?'Session closed.':'Command completed without text output.'));if(info.kind==='session')updateSessionResult(info,false);writeTerminal(info,'\r\n\x1b[2m-- '+(m.status||'completed')+' --\x1b[0m\r\n');es.close();if(info.es===es)info.es=null;}
  };
	  es.onerror=()=>{if(info.es!==es)return;info.streamErrors++;const known=info.kind==='job'?lastJobs[(info.job||{}).job_id]:lastSessions[(info.session||{}).session_id],archived=info.kind==='job'&&(!known||known.stream_available===false||TERMINAL.includes((known||info.job||{}).status)),permanent=!known||(info.kind==='session'&&!known._live);if(archived){es.close();info.es=null;archiveTermStream(info);return;}if(permanent||info.streamErrors>=3){es.close();info.es=null;setCurrentStreamState(info,'unavailable');setTermNotice(info,'error','Stream disconnected. Retry this run when the daemon is reachable.');return;}setCurrentStreamState(info,'reconnecting');};
}
function lastMatch(root,selector){const all=root.querySelectorAll(selector);return all.length?all[all.length-1]:null;}
function currentResponsePart(info,selector){const turn=lastMatch(info.host,'.response-turn');return turn&&turn.querySelector(selector);}
function setCurrentStreamState(info,key){const state=currentResponsePart(info,'.stream-state');if(state){state.dataset.streamState=key;state.textContent=tr(key);}}
function createChatParser(){try{const parser=new Terminal({cols:240,rows:2,scrollback:1000,convertEol:true,disableStdin:true});parser._lineFeedCount=0;parser._droppedOutput=false;parser.onLineFeed(()=>{parser._lineFeedCount++;if(parser._lineFeedCount>1000)parser._droppedOutput=true;});return parser;}catch(_){return null;}}
function resetChatParser(info){
  info.chatParser=createChatParser();
  const turn=lastMatch(info.host,'.response-turn');if(turn)turn._chatParser=info.chatParser;
}
function chatParserText(parser){
  if(!parser)return'';const buffer=parser.buffer.active;let text='';
  for(let i=0;i<buffer.length;i++){const line=buffer.getLine(i);if(i>0&&!line.isWrapped)text+='\n';text+=line.translateToString(true).replace(/[ \t]+$/,'');}
  text=text.replace(/\n+$/,'');return parser._droppedOutput?'[... earlier output omitted ...]\n'+text:text;
}
function renderChatOutputValue(info,out,value,preview){
  if(!out)return;let visible=value||'';preview=preview||'';
  if(preview&&!visible.trimEnd().endsWith(preview.trimEnd()))visible+=(visible&&!/[\r\n]$/.test(visible)?'\n':'')+preview;
  out.classList.toggle('output-empty',!visible);out.textContent=visible||(out.dataset.empty||'Waiting for output…');
  enforceTranscriptBudget(info,info.outputEl||out);
  const sc=info.host.querySelector('.transcript');if(sc&&sc.scrollHeight-sc.scrollTop-sc.clientHeight<160)sc.scrollTop=sc.scrollHeight;
}
function renderChatOutput(info){renderChatOutputValue(info,info.outputEl||currentResponsePart(info,'.chat-output'),info.output,info.promptPreview);}
function scheduleChatRender(info){if(info.renderFrame)return;info.renderFrame=requestAnimationFrame(()=>{info.renderFrame=0;renderChatOutput(info);});}
function stripRenderedPrompt(info,chunk){
  const prompt=info.skipPromptPrefix;if(!prompt)return chunk;
  const at=chunk.indexOf(prompt);if(at<0||at>32)return chunk;
  info.skipPromptPrefix='';
  return chunk.slice(0,at)+chunk.slice(at+prompt.length).replace(/^[ \t]*(?:\r?\n)?/,'');
}
function appendChatOutput(info,chunk){
  chunk=stripRenderedPrompt(info,chunk);if(!chunk)return;
  const max=160000;info.hadAnyOutput=true;info.hadCurrentOutput=true;
	  if(info.chatParser){const parser=info.chatParser;parser.write(chunk,()=>{if(info.chatParser!==parser)return;info.output=chatParserText(parser);if(info.output.length>max)info.output='[... earlier output omitted ...]\n'+info.output.slice(-max);scheduleChatRender(info);});}
	  else{info.output+=chunk;if(info.output.length>max)info.output='[... earlier output omitted ...]\n'+info.output.slice(-max);scheduleChatRender(info);}
}
function finishChatParser(info,turn){
  const parser=turn&&turn._chatParser;if(!parser)return;
  const out=turn.querySelector('.chat-output'),preview=turn._promptPreview||'';turn._chatParser=null;if(info.chatParser===parser)info.chatParser=null;
  parser.write('',()=>{let output=chatParserText(parser);if(output.length>160000)output='[... earlier output omitted ...]\n'+output.slice(-160000);renderChatOutputValue(info,out,output,preview);try{parser.dispose();}catch(_){}});
}
function enforceTranscriptBudget(info,current){
  const outputs=Array.from(info.host.querySelectorAll('.chat-output'));let total=outputs.reduce((n,x)=>n+(x.textContent||'').length,0);
  for(const out of outputs){if(total<=240000)break;if(out===current||out.dataset.omitted==='true')continue;total-=(out.textContent||'').length;out.textContent='[Earlier output omitted]';out.dataset.omitted='true';total+=out.textContent.length;}
}
function pruneTranscript(info){
  const transcript=info.host.querySelector('.transcript');let turns=Array.from(transcript.querySelectorAll('.turn'));
  if(turns.length<=32)return;
  while(turns.length>28){const old=turns[1];if(!old)break;try{if(old._chatParser)old._chatParser.dispose();}catch(_){}old.remove();turns=Array.from(transcript.querySelectorAll('.turn'));}
  if(!transcript.querySelector('.transcript-truncated')){const note=document.createElement('div');note.className='transcript-truncated';note.textContent='Earlier conversation turns omitted.';transcript.querySelector('.request-turn').after(note);}
}
function setTermInputEnabled(info,enabled){info.canInput=!!enabled;try{if(info.term)info.term.options.disableStdin=!info.canInput;}catch(_){}if(!info.canInput){clearTimeout(info.rawTimer);info.rawTimer=null;info.rawBuffer='';}}
function updateResult(info,job){if(!info||!job)return;info.job=Object.assign({},info.job||{},job);const line=lastMatch(info.host,'.result-line'),badge=line&&line.querySelector('.badge'),detail=line&&line.querySelector('.result-detail'),reason=line&&line.querySelector('.result-reason');if(badge){badge.className='badge st-'+statusClass(info.job);badge.textContent=statusLabel(info.job);}if(detail)detail.textContent=info.job.duration_ms?duration(info.job.duration_ms):'';if(reason)reason.textContent=info.job.reason||'';const controls=info.host.querySelector('.takeover'),live=!TERMINAL.includes(info.job.status)&&info.job.stream_available!==false;if(controls){controls.hidden=!live;controls.querySelectorAll('button').forEach(x=>x.disabled=!live);}setTermInputEnabled(info,live);}
function updateSessionResult(info,activeNow){const line=lastMatch(info.host,'.result-line'),badge=line&&line.querySelector('.badge'),reason=line&&line.querySelector('.result-reason');if(badge){badge.className='badge '+(activeNow?'st-running':'st-exited');badge.textContent=tr(activeNow?'sessionActive':'sessionClosed');}if(reason)reason.textContent=tr(activeNow?'liveTerminal':'streamEnded');setTermInputEnabled(info,activeNow);}
function appendResponseTurn(info){
  const transcript=info.host.querySelector('.transcript'),previousTurn=lastMatch(transcript,'.response-turn'),sourceTurn=lastMatch(transcript,'.response-turn:not(.delivery-failed)'),previous=previousTurn&&previousTurn.querySelector('.result-line');if(previous)previous.hidden=true;
  if(previousTurn)previousTurn._promptPreview=info.promptPreview||'';renderChatOutput(info);info.skipPromptPrefix=info.promptText||'';info.promptPreview='';finishChatParser(info,previousTurn);
	  const turn=document.createElement('div');turn.className='turn response-turn';turn.innerHTML=`<div class="avatar terminal">&gt;_</div><div><div class="msg-head"><b data-i18n="responseContinued">${esc(tr('responseContinued'))}</b><span class="stream-state" data-stream-state="sendingAnswer">${esc(tr('sendingAnswer'))}</span></div><div class="message output-shell"><div class="output-gap">Earlier output is no longer available</div><pre class="chat-output output-empty" data-empty-key="waitingMore" data-empty="${esc(tr('waitingMore'))}">${esc(tr('waitingMore'))}</pre><div class="result-line"><span class="badge"></span><strong class="result-detail"></strong><span class="result-reason"></span></div></div></div>`;
  transcript.appendChild(turn);info.output='';info.outputEl=turn.querySelector('.chat-output');info.hadCurrentOutput=false;resetChatParser(info);if(info.job)updateResult(info,info.job);else updateSessionResult(info,true);pruneTranscript(info);
  return{turn,sourceState:sourceTurn&&sourceTurn.querySelector('.stream-state')};
}
function finishResponseTurn(boundary,sent){if(!boundary)return;const state=boundary.turn&&boundary.turn.querySelector('.stream-state');if(sent){if(boundary.sourceState){boundary.sourceState.dataset.streamState='answered';boundary.sourceState.textContent=tr('answered');}if(state&&state.dataset.streamState==='sendingAnswer'){state.dataset.streamState='live';state.textContent=tr('live');}return;}if(boundary.turn)boundary.turn.classList.add('delivery-failed');if(state&&!['completed','unavailable','archived'].includes(state.dataset.streamState)){state.dataset.streamState='answerNotDelivered';state.textContent=tr('answerNotDelivered');}}
function cssId(k){return k.replace(/[^a-z0-9]/gi,'_');}
function focusTab(key){
  active=key;
  for(const k in terms)terms[k].host.classList.toggle('active',k===key);
  const t=terms[key];if(t&&t.view==='terminal'&&t.fit)setTimeout(()=>{try{t.fit.fit();t.term.focus();}catch(_){}} ,30);
  document.getElementById('nudge').style.display=active?'none':'flex';
  renderWorkspace();syncSidebarSelection();
}
function setTermView(key,view){const info=terms[key];if(!info)return;info.view=view;info.host.classList.toggle('mode-terminal',view==='terminal');info.host.querySelectorAll('.view-switch button').forEach(b=>{const selected=b.dataset.view===view;b.classList.toggle('active',selected);b.setAttribute('aria-pressed',String(selected));});if(view==='terminal'){ensureTerminal(info);setTimeout(()=>{try{if(info.fit)info.fit.fit();info.term.focus();}catch(_){}} ,20);}else{const log=info.host.querySelector('.transcript');if(log)log.focus();}}
function disposeTerm(key){
  const t=terms[key]; if(!t) return;
	  void flushRawTermInput(t);
	  if(t.renderFrame)cancelAnimationFrame(t.renderFrame);
  try{if(t.es)t.es.close();}catch(_){}
  t.host.querySelectorAll('.response-turn').forEach(turn=>{try{if(turn._chatParser)turn._chatParser.dispose();}catch(_){}});
	  t.host.remove();if(t.term)t.term.dispose();delete terms[key];if(active===key)active=null;
}
function closeTerm(key){
  disposeTerm(key);const rest=Object.keys(terms);if(rest.length)focusTab(rest[0]);else{document.getElementById('nudge').style.display='flex';renderWorkspace();}syncSidebarSelection();
}

function openSession(id){
  const session=lastSessions[id]||{session_id:id,target:'local',owner:'agent'};
	  const route=routeForSession(session);activeConversationKey=conversationKey(route.owner,route.target,route.workspace);
  makeTerm({key:'sess:'+id,kind:'session',session,controlsHTML:'',streamPath:'/api/session/stream?session_id='+encodeURIComponent(id),
    onInput:(d,o={})=>api('/api/session/write',{method:'POST',body:JSON.stringify({session_id:id,input:d,append_newline:!!o.appendNewline,human:true,secret:!!o.secret})}),
    promptInput:(d,o={})=>{const info=terms['sess:'+id],jobID=info&&info.promptJobID;if(!jobID)return Promise.resolve({error:{message:'The request ended or changed. Refresh the conversation and try again.'}});return api('/api/exec/write',{method:'POST',body:JSON.stringify({job_id:jobID,input:d,append_newline:!!o.appendNewline,human:true,secret:!!o.secret})});}});
  renderWorkspace();refreshDialogTree();setSidebarOpen(false,'main');
}
function openJob(job){
  const id=job.job_id;
	  const route=routeForJob(job);activeConversationKey=conversationKey(route.owner,route.target,route.workspace);
	  const controls=TERMINAL.includes(job.status)?'':`<span class="hold-tag" id="ht-${esc(id)}"></span><button class="ghost" id="hi-${esc(id)}" data-action="toggle-hold" data-id="${esc(id)}" data-kind="input" aria-pressed="false">${esc(tr('blockInput'))}</button><button class="ghost" id="ho-${esc(id)}" data-action="toggle-hold" data-id="${esc(id)}" data-kind="output" aria-pressed="false">${esc(tr('pauseOutput'))}</button><button class="danger" data-action="kill-job" data-id="${esc(id)}">${esc(tr('stop'))}</button>`;
  makeTerm({key:'job:'+id,kind:'job',job,controlsHTML:controls,streamPath:'/api/exec/stream?job_id='+encodeURIComponent(id),
    onInput:(d,o={})=>api('/api/exec/write',{method:'POST',body:JSON.stringify({job_id:id,input:d,append_newline:!!o.appendNewline,human:true,secret:!!o.secret})}),promptInput:(d,o={})=>api('/api/exec/write',{method:'POST',body:JSON.stringify({job_id:id,input:d,append_newline:!!o.appendNewline,human:true,secret:!!o.secret})})});
  renderWorkspace();refreshDialogTree();setSidebarOpen(false,'main');
}
async function openJobById(id){ let j=lastJobs[id]; if(j) openJob(j); }
function preferredConversationJob(group){if(!group)return null;const currentIDs=new Set(group.sessions.map(session=>session.current_job_id).filter(Boolean)),activeJobs=group.jobs.filter(job=>executionCategory(job)==='active');return activeJobs.find(job=>job.awaiting_input)||activeJobs.find(job=>currentIDs.has(job.job_id))||activeJobs[0]||group.jobs[0]||null;}
function openConversation(key){
  const group=conversationGroup(key);if(!group)return;const current=active&&terms[active];if(activeConversationKey!==key)executionFilter='all';activeConversationKey=key;renderWorkspace();refreshDialogTree();
  if(current&&termConversationKey(current)===key){focusTab(active);setSidebarOpen(false,'main');return;}
  const job=preferredConversationJob(group);if(job){openJob(job);return;}const session=group.sessions[0];if(session){openSession(session.session_id);return;}Object.keys(terms).forEach(disposeTerm);renderWorkspace();setSidebarOpen(false,'main');
}
function beginOperatorReply(info,value,secret){
  const transcript=info.host.querySelector('.transcript'),turn=document.createElement('div');turn.className='turn operator-turn';
  turn.innerHTML='<div class="avatar system">YOU</div><div><div class="msg-head"><b>Your answer</b><span class="reply-state">sending…</span></div><div class="message"><div class="request-text"></div></div></div>';
  turn.querySelector('.request-text').textContent=secret?'Secret answer hidden':(value===''?'Enter pressed':value);
  transcript.appendChild(turn);const boundary=appendResponseTurn(info);transcript.scrollTop=transcript.scrollHeight;pruneTranscript(info);return{turn,boundary};
}
function finishOperatorReply(turn,sent){if(!turn)return;const state=turn.querySelector('.reply-state');if(state)state.textContent=sent?'sent just now':'not sent';turn.classList.toggle('failed',!sent);}
async function sendPromptAnswer(key,answer){
  const info=terms[key];if(!info||info.sendingPrompt)return;const input=info.host.querySelector('.prompt-input');if(answer==null&&!input)return;
  const value=answer!=null?answer:input.value,secret=answer==null&&!!info.promptSecret,send=info.promptInput||info.onInput;
  info.sendingPrompt=true;info.host.querySelectorAll('.prompt-actions button,.prompt-actions input').forEach(x=>x.disabled=true);
  const reply=beginOperatorReply(info,value,secret);
  try{await queueTermInput(info,send,value,{appendNewline:true,secret,clearNotice:false});finishOperatorReply(reply.turn,true);finishResponseTurn(reply.boundary,true);info.inputFailed=false;setTermNotice(info,'','');showToast(secret?'Secret answer sent':'Answer sent');}
  catch(err){finishOperatorReply(reply.turn,false);finishResponseTurn(reply.boundary,false);if(!info.inputFailed){info.inputFailed=true;setTermNotice(info,'error',err&&err.message?err.message:String(err));}}
  finally{info.sendingPrompt=false;}
}
async function toggleHold(id,kind){
  const info=lastJobs[id]||{}; const cur=kind==='input'?info.hold_input:info.hold_output;
  const body={job_id:id}; body[kind==='input'?'hold_input':'hold_output']=!cur;
  const r=await api('/api/exec/hold',{method:'POST',body:JSON.stringify(body)});if(r&&r.error){showToast(errorText(r,'Could not change takeover mode'),'error');return;}refresh();
}
async function killJob(id){const r=await api('/api/exec/kill',{method:'POST',body:JSON.stringify({job_id:id})});if(r&&r.error){showToast(errorText(r,'Could not stop command'),'error');return;}showToast('Command stopped');refresh();}

// ---- server management (human-only, via dashboard) ------------------------
let srvStatus={};
const val=id=>document.getElementById(id).value.trim();
function toggleServerForm(){ const f=document.getElementById('server-form'); f.style.display=f.style.display==='block'?'none':'block'; }
async function addServer(){
  const msg=document.getElementById('sf-msg');
  const set=(c,t)=>{ msg.style.color='var(--'+c+')'; msg.textContent=t; };
  const name=val('sf-name'), host=val('sf-host'), user=val('sf-user'),
        secret=document.getElementById('sf-secret').value, pass=document.getElementById('sf-vault').value;
  if(!name||!host||!user){ set('red','✗ name, host and user are required'); return; }
  // An SSH key/password is kept in the encrypted vault, which a passphrase opens.
  if(secret && !pass){
    set('yellow','🔒 Pick a vault passphrase below — it encrypts this credential. You choose it the first time, then reuse it to add more servers.');
    document.getElementById('sf-vault').focus(); return;
  }
  set('muted','adding…');
  if(secret && pass){
    const u=await api('/api/vault/unlock',{method:'POST',body:JSON.stringify({passphrase:pass})});
    if(u&&u.error){ set('red','✗ vault didn’t open: '+(u.error.message||u.error.code)+' — the passphrase must match the one you first set. Forgot it? Reset with: termada vault reset'); return; }
  }
  const body={name,host,user,port:parseInt(val('sf-port'))||0,
    tags:val('sf-tags').split(',').map(t=>t.trim()).filter(Boolean), secret};
  const r=await api('/api/servers/add',{method:'POST',body:JSON.stringify(body)});
  if(r&&r.error){
    if(r.error.code==='vault_locked'){ set('yellow','🔒 Enter a vault passphrase below to encrypt the credential (you set it on first use).'); document.getElementById('sf-vault').focus(); }
    else set('red','✗ '+(r.error.message||r.error.code));
    return;
  }
  set('green','✓ added');
  ['sf-name','sf-host','sf-user','sf-port','sf-tags','sf-secret','sf-vault'].forEach(i=>document.getElementById(i).value='');
  toggleServerForm(); await refresh(); setTimeout(()=>runAction(testServer(name),'Could not test server'),300);
}
async function testServer(name){
  srvStatus[name]='testing'; refresh();
  const r=await api('/api/servers/test',{method:'POST',body:JSON.stringify({name})});
  let st='unknown';
  if(r){ if(typeof r.status==='string'&&r.status) st=r.status; else if(r.error&&typeof r.error==='object') st=r.error.code||'error'; }
  srvStatus[name]=st; refresh();
}
async function removeServer(name){
  if(!confirm('Remove server '+name+'?'))return;
  const r=await api('/api/servers/remove',{method:'POST',body:JSON.stringify({name})});
  if(r&&r.error){showToast(errorText(r,'Could not remove server'),'error');return;}
  delete srvStatus[name]; refresh();
}

// ---- inbox tree / status --------------------------------------------------
let lastJobs={},lastSessions={},lastPending=[],lastDialogSessions=[];
const approvalState={};
function approvalWait(p){const expires=timeSec(p,'expires');if(expires){const left=Math.max(0,expires-Date.now()/1000);return left?Math.ceil(left/60)+'m until auto-deny':'expired';}return p.waiting_ms?duration(p.waiting_ms)+' waiting':'';}
function renderAttention(pending,jobs){
  lastPending=pending;
  const activeConfirmations=new Set(pending.map(p=>p.confirmation_id));Object.keys(approvalState).forEach(id=>{if(!activeConfirmations.has(id))delete approvalState[id];});
  const prompts=jobs.filter(j=>j.awaiting_input&&j.status!=='awaiting_confirmation');
  const count=pending.length+prompts.length,attention=document.getElementById('attention-section'),container=document.getElementById('approvals');document.getElementById('attention-count').textContent=count;attention.hidden=count===0;
  if(!count){renderStableHTML(container,'');return;}
  const approvals=pending.map(p=>{const state=approvalState[p.confirmation_id]||{},session=lastSessions[p.session_id]||{},owner=displayOwner(canonicalOwner(p.agent_id||session.owner)),target=displayTarget(p.target||session.target||UNKNOWN_TARGET),reason=approvalReason(p.reason||'');
    return `<div class="approve-card" role="group" aria-label="Approval for ${esc(owner)}"><div class="approve-head"><b>Approval required</b><span class="approve-wait">${esc(approvalWait(p))}</span></div>
      <div class="approve-route"><b>${esc(owner)}</b> → ${esc(target)}</div><div class="cmd mono">${esc(displayCommand(p.command))}</div>
      ${reason||p.matched?`<div class="approve-rule">${reason?'Reason: '+esc(reason):''}${p.matched?(reason?' · ':'')+'Rule: '+esc(p.matched):''}</div>`:''}
      <div class="row"><button class="primary" data-action="resolve" data-kind="approve" data-id="${esc(p.confirmation_id)}" ${state.busy?'disabled':''}>${state.busy?'Sending…':'Allow once'}</button>
      <button class="danger" data-action="resolve" data-kind="deny" data-id="${esc(p.confirmation_id)}" ${state.busy?'disabled':''}>Deny</button></div>${state.error?`<div class="inline-error">${esc(state.error)}</div>`:''}</div>`;
  }).join('');
  const promptCards=prompts.map(j=>{const route=routeForJob(j);return `<div class="approve-card"><div class="approve-head"><b>Your answer is needed</b><span class="approve-wait" data-relative-time="${esc(timeSec(j))}"></span></div>
    <div class="approve-route"><b>${esc(displayOwner(route.owner))}</b> → ${esc(displayTarget(route.target))}</div><div class="cmd mono">${esc(displayCommand(j.command))}</div>
    <div class="approve-rule">${esc(j.prompt||'The program is waiting for input')}</div><div class="row"><button class="primary" data-action="open-job" data-id="${esc(j.job_id)}">Open and answer</button></div></div>`;}).join('');
  renderStableHTML(container,approvals+promptCards);updateRelativeTimes(container);
}
function refreshDialogTree(){renderDialogTree(lastDialogSessions,Object.values(lastJobs));}
function renderDialogTree(sessions,jobs){
  const rows=buildConversationGroups(sessions,jobs);lastConversationGroups=rows;if(activeConversationKey&&!conversationGroup(activeConversationKey))activeConversationKey='';
  document.getElementById('dialog-count').textContent=rows.length;
  document.getElementById('s-sess').textContent=rows.length;
  const query=conversationFilter.trim().toLowerCase(),filtered=rows.filter(group=>{if(!query)return true;const base=(displayOwner(group.owner)+' '+displayWorkspace(group.workspace)+' '+displayTarget(group.target)).toLowerCase();return base.includes(query)||group.jobs.some(job=>(displayCommand(job.command)+' '+statusLabel(job)).toLowerCase().includes(query));});
	  const html=filtered.length?filtered.map(group=>{const jobs=group.jobs,activeJobs=jobs.filter(job=>executionCategory(job)==='active'),latest=jobs[0],needs=activeJobs.some(job=>job.awaiting_input),failed=jobFailed(latest),state=needs?'needs':activeJobs.length?'running':failed?'failed':latest?'done':'idle',stateTitle=needs?tr('needsInput'):activeJobs.length?tr('running'):failed?tr('failed'):latest?tr('completed'):tr('pending'),summary=latest?displayCommand(latest.command):tr('noRuns'),alert=needs?tr('needsInput'):activeJobs.length?activeJobs.length+' '+tr('active'):failed?tr('failed'):'',route=(group.workspace?displayWorkspace(group.workspace)+' · ':'')+displayOwner(group.owner)+' → '+displayTarget(group.target),selected=group.key===activeConversationKey,runLabel=runCountLabel(jobs.length);
    return `<div class="dialog-group" data-conversation-key="${esc(group.key)}"><div class="conversation-row"><button class="conversation-open${selected?' sel':''}" data-action="open-conversation" data-conversation-key="${esc(group.key)}" title="${esc(route+' · '+summary)}"${selected?' aria-current="page"':''}><span class="conversation-state ${state}" title="${esc(stateTitle)}" aria-hidden="true"></span><span class="conversation-route">${esc(route)}</span><span class="conversation-time">${alert?`<span class="conversation-alert ${state}">${esc(alert)}</span>`:`<span data-relative-time="${esc(timeSec(latest||group.sessions[0]))}"></span>`}</span><span class="conversation-summary">${esc(summary)}</span></button><button class="conversation-runs" data-action="open-conversation" data-conversation-key="${esc(group.key)}" aria-label="Open ${esc(runLabel)} for ${esc(route)}" title="Open runs"><strong>${esc(jobs.length)}</strong>${esc(runWord(jobs.length))}</button></div></div>`;
	  }).join(''):`<div class="empty">${esc(query?tr('noMatchingWorkspaces'):tr('noWorkspaces'))}</div>`;
  const container=document.getElementById('sessions');renderStableHTML(container,html);updateRelativeTimes(container);renderWorkspace();syncSidebarSelection();
}
let refreshInFlight=null,refreshQueued=false;
function refresh(){
	  if(refreshInFlight){refreshQueued=true;return refreshInFlight;}
	  clearTimeout(fallbackTimer);
	  refreshInFlight=refreshOnce().finally(()=>{refreshInFlight=null;if(refreshQueued){refreshQueued=false;void refresh();return;}armFallbackRefresh();});
	  return refreshInFlight;
}
function armFallbackRefresh(){clearTimeout(fallbackTimer);if(gated||document.hidden)return;fallbackTimer=setTimeout(()=>{void refresh();},activeJobsCount>0?2000:45000);}
function scheduleStateRefresh(delay=80){clearTimeout(stateRefreshTimer);stateRefreshTimer=setTimeout(()=>{if(!gated&&!document.hidden)void refresh();},delay);}
async function refreshOnce(){
	  let s;
	  try{s=await api('/api/dashboard/state?limit=100');if(s.error)throw new Error(errorText(s,'status error'));setConnectionState('connected');document.getElementById('dot').classList.add('live');}
  catch(e){
	    setConnectionState(gated?'locked':'offline');
    document.getElementById('dot').classList.remove('live'); return; }
  document.getElementById('ver').textContent='v'+(s.version||'?');

	  stateRevision=Math.max(stateRevision,Number(s.state_revision||0));const jobs=(s.jobs||[]).slice().sort((a,b)=>timeMS(b)-timeMS(a));lastJobs={};lastSessions={};lastJobsOmitted=Number(s.jobs_omitted||0);lastDialogSessions=(s.sessions||[]).map(session=>Object.assign({_live:true},session));jobs.forEach(j=>lastJobs[j.job_id]=j);lastDialogSessions.forEach(x=>lastSessions[x.session_id]=x);
	  const active_jobs=jobs.filter(j=>!TERMINAL.includes(j.status)&&j.status!=='awaiting_confirmation');
	  activeJobsCount=active_jobs.length;
  document.getElementById('s-jobs').textContent=active_jobs.length;
  document.getElementById('s-pend').textContent=(s.pending||[]).length+jobs.filter(j=>j.awaiting_input&&j.status!=='awaiting_confirmation').length;
  renderAttention(s.pending||[],jobs);renderDialogTree(s.sessions||[],jobs);

  const serverHTML=(s.servers&&s.servers.length)? s.servers.map(x=>{
    const st=srvStatus[x.name]||x.status||'';
    // The health-check result is only as fresh as the last check (~30s cadence);
    // grey a stale 'ok' dot and surface "checked Xago" so a lagging/stalled check
    // can't masquerade as live.
    const stale=x.checked_unix&&(Date.now()/1000-x.checked_unix)>90;
    let c={ok:'#3fb950',testing:'#d29922'}[st]||(st?'#f85149':'#8b949e');
    if(stale) c='#8b949e';
    const dot=st==='testing'?'…':'●';
    const checked=x.checked_unix?'checked at '+new Date(x.checked_unix*1000).toLocaleTimeString():'not checked yet',statusText=st?serverStatusLabel(st):'';
    return `<div class="item srv" title="${esc((st||'click test')+' · '+checked)}">
      <span class="srv-name"><span class="sdot" style="color:${c}">${dot}</span><b>${esc(x.name)}</b>
        <span class="muted">${esc(x.user)}@${esc(x.host)}${statusText?' · '+esc(statusText):''}${stale?' · <span style="color:var(--yellow)">stale</span>':''}</span></span>
      ${(x.tags||[]).map(t=>'<span class="badge">'+esc(t)+'</span>').join('')}
      <span style="flex:1"></span>
      <button class="mini" data-action="test-server" data-name="${esc(x.name)}" title="${esc(tr('test'))} ${esc(x.name)}">${esc(tr('test'))}</button>
      ${x.managed?`<button class="mini" data-action="remove-server" data-name="${esc(x.name)}" aria-label="Remove server ${esc(x.name)}" title="Remove ${esc(x.name)}">✕</button>`:''}
    </div>`; }).join('') : '<div class="empty">'+esc(tr('noServers'))+'</div>';
  renderStableHTML(document.getElementById('servers'),serverHTML);

  // agents: who connected, how often, what they did
  const agentHTML=(s.agents&&s.agents.length)? s.agents.map(a=>{
    const online=(Date.now()/1000 - (a.last_seen_unix||0)) < 60;
    const hist=(a.history||[]).slice().reverse().map(h=>'• '+h).join('\n');
    const titleAttr=`first seen ${a.first_seen_unix?new Date(a.first_seen_unix*1000).toLocaleString():'unknown'}${hist?'\nrecent:\n'+hist:''}`;
    return `<div class="item srv" title="${esc(titleAttr)}">
        <span class="srv-name agent-name"><span class="sdot" style="color:${online?'#3fb950':'#8b949e'}">●</span>${esc(a.id)}
          <span class="muted">${online?esc(tr('online')):esc(tr('idle'))+' · <span data-relative-time="'+esc(a.last_seen_unix)+'"></span>'}</span></span>
        <span class="badge">${esc(a.jobs||0)} ${esc(tr('jobsLabel'))}</span>
        ${a.sessions?`<span class="badge">${esc(a.sessions)} ${esc(tr('sessionsLabel'))}</span>`:''}
        ${a.denied?`<span class="badge st-failed">${esc(a.denied)} ${esc(tr('deniedLabel'))}</span>`:''}
      </div>${a.last_command?`<div class="amini" title="${esc(hist)}">↳ ${esc(a.last_command)} · <span data-relative-time="${esc(a.last_command_unix||a.last_seen_unix)}"></span></div>`:''}`;
  }).join('') : '<div class="empty">'+esc(tr('noConnections'))+'</div>';
  const agentsEl=document.getElementById('agents');renderStableHTML(agentsEl,agentHTML);updateRelativeTimes(agentsEl);

  for(const key in terms){if(key.startsWith('sess:')){const session=lastSessions[key.slice(5)];if(session)terms[key].session=session;updateSessionResult(terms[key],!!(session&&session._live));continue;}if(!key.startsWith('job:'))continue;const id=key.slice(4);const j=lastJobs[id];if(!j)continue;
    const hi=document.getElementById('hi-'+id), ho=document.getElementById('ho-'+id), ht=document.getElementById('ht-'+id);
    if(hi){hi.className=j.hold_input?'on':'ghost';hi.setAttribute('aria-pressed',String(!!j.hold_input));}if(ho){ho.className=j.hold_output?'warn':'ghost';ho.setAttribute('aria-pressed',String(!!j.hold_output));}
    if(ht)ht.textContent=(j.hold_input?'Agent input blocked ':'')+(j.hold_output?'Agent output paused':'');
    updateResult(terms[key],j);if(!terms[key].inputFailed)setTermNotice(terms[key],j.awaiting_input?'prompt':'',j.awaiting_input?(j.prompt||'Input is required'):''); }
}
async function resolve(kind,id){if(!id||approvalState[id]&&approvalState[id].busy)return;approvalState[id]={busy:true,error:''};renderAttention(lastPending,Object.values(lastJobs));try{const r=await api('/api/'+kind,{method:'POST',body:JSON.stringify({confirmation_id:id})});if(r&&r.error)throw new Error(errorText(r,'Decision was not accepted'));delete approvalState[id];showToast(kind==='approve'?'Command approved and starting':'Command denied');await refresh();}catch(err){approvalState[id]={busy:false,error:err&&err.message?err.message:String(err)};renderAttention(lastPending,Object.values(lastJobs));showToast('Could not apply decision','error');}}
async function stopAll(){if(!confirm('Stop all active commands?'))return;const r=await api('/api/stop_all',{method:'POST'});if(r&&r.error){showToast(errorText(r,'Could not stop commands'),'error');return;}showToast('Commands stopped: '+(r.killed||0));refresh();}

// ---- policies (add / edit / delete the allow/deny/confirm rules) ----------
let lastPolicies={};
const lines=id=>document.getElementById(id).value.split('\n').map(s=>s.trim()).filter(Boolean);
function togglePolicyForm(){ const f=document.getElementById('policy-form'); const show=f.style.display!=='block';
  f.style.display=show?'block':'none'; if(!show){clearPolicyForm();} }
function clearPolicyForm(){ ['pf-name','pf-allow','pf-deny','pf-confirm'].forEach(i=>document.getElementById(i).value='');
  document.getElementById('pf-msg').textContent=''; }
function editPolicy(name){
  const po=lastPolicies[name]||{};
  document.getElementById('pf-name').value=name;
  document.getElementById('pf-allow').value=(po.allow||[]).join('\n');
  document.getElementById('pf-deny').value=(po.deny||[]).join('\n');
  document.getElementById('pf-confirm').value=(po.confirm||[]).join('\n');
  document.getElementById('pf-msg').textContent='';
  document.getElementById('policy-form').style.display='block';
  document.getElementById('pf-name').focus();
}
async function savePolicy(){
  const msg=document.getElementById('pf-msg'); const set=(c,t)=>{msg.style.color='var(--'+c+')';msg.textContent=t;};
  const name=val('pf-name');
  if(!name){ set('red','✗ name is required'); return; }
  set('muted','saving…');
  const body={name, allow:lines('pf-allow'), deny:lines('pf-deny'), confirm:lines('pf-confirm')};
  const r=await api('/api/policies/set',{method:'POST',body:JSON.stringify(body)});
  if(r&&r.error){ set('red','✗ '+(r.error.message||r.error.code)); return; }
  set('green','✓ saved'); clearPolicyForm();
  document.getElementById('policy-form').style.display='none';
  loadPolicies();
}
async function deletePolicy(name){
  if(!confirm('Delete policy "'+name+'"? Agents using it fall back to allow-all.'))return;
  const r=await api('/api/policies/remove',{method:'POST',body:JSON.stringify({name})});
  if(r&&r.error){ alert(r.error.message||r.error.code); return; }
  loadPolicies();
}
async function loadPolicies(){
  let p={}; try{ p=await api('/api/policies'); }catch(e){ return; }
  const pols=p.policies||{}, agents=p.agents||{}, managed=p.managed||{};
  lastPolicies=pols;
  const byPol={}; for(const a in agents){ (byPol[agents[a]]=byPol[agents[a]]||[]).push(a); }
  const names=Object.keys(pols).sort();
  const el=document.getElementById('policies');
  el.innerHTML = names.length ? names.map(n=>{
    const po=pols[n]||{}, rules=[];
    if((po.allow||[]).length) rules.push('<span class="tag st-exited">allow</span> '+po.allow.map(esc).join(', '));
    if((po.deny||[]).length) rules.push('<span class="tag st-killed">deny</span> '+po.deny.map(esc).join(', '));
    if((po.confirm||[]).length) rules.push('<span class="tag st-awaiting_input">confirm</span> '+po.confirm.map(esc).join(', '));
    const who=(byPol[n]||[]).map(esc).join(', ');
    const ctl = managed[n]
      ? `<span class="ctl"><button data-action="edit-policy" data-name="${esc(n)}">edit</button><button class="danger" data-action="delete-policy" data-name="${esc(n)}" aria-label="Delete policy ${esc(n)}" title="Delete ${esc(n)}">✕</button></span>`
      : `<span class="ctl"><span class="tag cfg" title="defined in config.yaml — edit the file to change">config</span></span>`;
    return `<div class="pol">${ctl}<b>${esc(n)}</b>${who?' <span class="muted">· '+who+'</span>':''}<div class="pr">${rules.join('<br>')||'<span class="muted">no rules → allow all</span>'}</div></div>`;
  }).join('') : '<div class="empty">No policies configured</div>';
}

// ---- history / replay (persistent hash-chained audit) --------------------
let auditRecs=[],auditFilter='all';
function auditActor(record){return record&&record.data&&(record.data.actor||record.data.source)||record&&record.agent_id||tr('operator');}
function auditTime(record){const value=record&&record.time?Date.parse(record.time):0;return Number.isFinite(value)?value:0;}
function auditCategory(record){const type=record&&record.type||'';if(/^confirm\.|^human_input\./.test(type))return'attention';if(/^job\./.test(type)||record&&record.job_id)return'runs';if(/^agent\.|^session\./.test(type))return'connections';return'system';}
function auditMessage(record){
  if(!record)return'';
  if(/^human_input\./.test(record.type||'')){const hidden=record.data&&record.data.secret,bytes=record.data&&record.data.byte_count;return(hidden?tr('secretAnswer'):tr('inputNeeded'))+(bytes!=null?' · '+bytes+' bytes':'');}
  let message=record.message||record.type||'';
  if(record.data&&record.data.matched)message+=' ['+record.data.matched+']';
  return message;
}
function buildAuditGroups(records){
  const groups=[],byKey=new Map(),pendingStarts=[],ordered=records.map((record,index)=>({record,index})).sort((a,b)=>auditTime(a.record)-auditTime(b.record));
  ordered.forEach(({record,index})=>{
    const category=auditCategory(record),actor=auditActor(record),type=record.type||'',jobID=record.job_id||record.data&&record.data.job_id||'',key=jobID?'job:'+jobID:record.session_id&&/^session\./.test(type)?'session:'+record.session_id:/^agent\./.test(type)?'agent:'+actor:'record:'+index;
    let group=byKey.get(key);if(!group){group={key,category,actor,records:[],latest:0};byKey.set(key,group);groups.push(group);}
    if(type==='job.started'&&jobID){for(let at=pendingStarts.length-1;at>=0;at--){const pending=pendingStarts[at],age=auditTime(record)-pending.time;if(age>10000)break;if(pending.actor===actor&&pending.session===(record.session_id||'')&&pending.message===(record.message||'')){group.records.push(...pending.group.records);groups.splice(groups.indexOf(pending.group),1);pendingStarts.splice(at,1);break;}}}
    group.records.push(record);group.latest=Math.max(group.latest,auditTime(record));if(category==='attention')group.category='attention';
    if(type==='job.start_requested'&&!jobID)pendingStarts.push({group,actor,session:record.session_id||'',message:record.message||'',time:auditTime(record)});
  });
  groups.forEach(group=>group.records.sort((a,b)=>auditTime(b)-auditTime(a)));
  return groups.sort((a,b)=>b.latest-a.latest);
}
function auditGroupTitle(group){
  const command=group.records.find(record=>/^job\.(?:start_requested|started)$/.test(record.type||'')&&record.message);
  if(command)return command.message;
  if(group.key.startsWith('agent:'))return group.actor;
  const first=group.records[0]||{};
  if(group.key.startsWith('session:'))return displayOwner(first.agent_id||group.actor)+' · '+short(first.session_id,12);
  return auditMessage(first)||tr(group.category==='connections'?'connection':group.category==='system'?'systemActivity':'execution');
}
function auditGroupState(group){
  const types=group.records.map(record=>record.type||''),latest=group.records[0]||{},message=(latest.message||'').toLowerCase();
  if(group.category==='attention'&&/^confirm\.requested$/.test(latest.type||''))return{kind:'needs',label:tr('needsApproval')};
  if(types.includes('persistence.error')||types.includes('job.killed')||/(?:failed|error|orphaned|timed out)/.test(message))return{kind:'failed',label:tr('failed')};
  if(types.includes('job.finished'))return{kind:'done',label:tr('completed')};
  if(types.includes('job.started'))return{kind:'running',label:tr('running')};
  return{kind:'idle',label:group.category==='connections'?tr('connection'):tr(group.category==='system'?'system':'events')};
}
async function openHistory(){
  document.getElementById('history').style.display='flex';
  document.getElementById('hist-list').innerHTML='<div class="empty">'+esc(tr('waitingOutput'))+'</div>';
  let r={}; try{ r=await api('/api/audit?n=500'); }catch(e){}
  auditRecs = (r&&r.records) || (Array.isArray(r)?r:[]) || [];
  renderAudit();setTimeout(()=>document.getElementById('hist-filter').focus(),0);
}
function closeHistory(){ document.getElementById('history').style.display='none'; }
function setAuditFilter(filter){auditFilter=['all','runs','attention','connections','system'].includes(filter)?filter:'all';document.querySelectorAll('.hist-filters button').forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.filter===auditFilter)));renderAudit();}
function renderAudit(){
  const q=(document.getElementById('hist-filter').value||'').trim().toLowerCase(),allGroups=buildAuditGroups(auditRecs);
  let groups=allGroups.filter(group=>(auditFilter==='all'||group.category===auditFilter)&&(!q||JSON.stringify(group.records).toLowerCase().includes(q)||auditGroupTitle(group).toLowerCase().includes(q)));
  const visibleEvents=groups.reduce((total,group)=>total+group.records.length,0);
  document.getElementById('hist-meta').textContent=groups.length+' '+tr('activities')+' · '+visibleEvents+' '+tr('events')+(groups.length!==allGroups.length?' · '+allGroups.length+' '+tr('loaded'):'');
  const list=document.getElementById('hist-list');
  list.innerHTML=groups.length?groups.map(group=>{const state=auditGroupState(group),shown=group.records.slice(0,20),hidden=group.records.length-shown.length,stamp=group.latest?new Date(group.latest):null,time=stamp?stamp.toLocaleDateString(undefined,{month:'short',day:'numeric'})+' · '+stamp.toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit'}):'';
    const rows=shown.map(record=>`<div class="activity-event"><span class="at">${record.time?esc(new Date(record.time).toLocaleTimeString()):''}</span><span class="ay">${esc(record.type||'')}</span><span class="am">${esc(auditMessage(record))}${record.session_id?' <span class="muted">· '+esc(short(record.session_id,12))+'</span>':''}</span></div>`).join('');
    return `<details class="activity-group"><summary><span class="activity-state ${state.kind}" aria-hidden="true"></span><span class="activity-main"><strong>${esc(short(auditGroupTitle(group),180))}</strong><span>${esc(group.actor)} · ${esc(state.label)}</span></span><span class="activity-count">${group.records.length}</span><time>${esc(time)}</time></summary><div class="activity-events">${rows}${hidden?`<div class="activity-hidden">${hidden} ${esc(tr('olderEventsHidden'))}</div>`:''}</div></details>`;
  }).join(''):`<div class="empty">${esc(tr('noActivity'))}</div>`;
}
const feed=document.getElementById('feed');
let feedES=null;
const feedSeen=new Set();
function startFeed(){
	  if(feedES){ try{feedES.close();}catch(_){ } }
	  const es=new EventSource(withToken('/api/events?since='+encodeURIComponent(stateRevision)));
	  feedES=es;
	  es.onmessage=ev=>{ let e; try{e=JSON.parse(ev.data);}catch(_){return;}
	    const sequence=Number(e.sequence||ev.lastEventId||0),eventKey=sequence?'seq:'+sequence:JSON.stringify([e.time,e.type,e.agent_id,e.session_id,e.job_id,e.message,e.data]);if(sequence&&sequence<=stateRevision)return;if(sequence)stateRevision=sequence;if(feedSeen.has(eventKey))return;feedSeen.add(eventKey);if(feedSeen.size>500)feedSeen.delete(feedSeen.values().next().value);
	    if(feed&&!feed.hidden){const d=document.createElement('div');d.className='e';const t=new Date(e.time||Date.now()).toLocaleTimeString(),ts=document.createElement('span'),ty=document.createElement('span'),msg=document.createElement('span');ts.className='t';ts.textContent=t;ty.className='ty';ty.textContent=e.type||'';msg.className='cmd';msg.textContent=e.message||'';d.append(ts,ty,msg);feed.prepend(d);while(feed.children.length>80)feed.removeChild(feed.lastChild);}
	    if(e.type==='state.resync_required')scheduleStateRefresh(0);
	    else if(['job.start_requested','job.started','job.finished','job.killed','confirm.requested','confirm.resolved','session.created','session.closed','session.reset','job.hold','human_input.authorized','agent.connected','persistence.error'].includes(e.type))scheduleStateRefresh();
	  };
	  es.onerror=()=>{if(feedES===es)setConnectionState('reconnecting');};
}

document.addEventListener('click',e=>{
  const el=e.target.closest&&e.target.closest('[data-action]');
  if(!el) return;
  const id=el.dataset.id||'', name=el.dataset.name||'';
	  switch(el.dataset.action){
	  case 'toggle-locale': toggleLocale(); break;
  case 'toggle-sidebar': setSidebarOpen(!sidebarOpen,sidebarOpen?'toggle':''); break;
  case 'close-sidebar': setSidebarOpen(false,'toggle'); break;
  case 'set-sidebar-view': setSidebarView(el.dataset.view||'conversations',false); break;
  case 'submit-gate': submitGate(); break;
  case 'open-history': openHistory(); break;
  case 'close-history': closeHistory(); break;
  case 'set-audit-filter': setAuditFilter(el.dataset.filter||'all'); break;
  case 'stop-all': runAction(stopAll(),'Could not stop commands'); break;
  case 'toggle-server-form': toggleServerForm(); break;
  case 'add-server': runAction(addServer(),'Could not add server'); break;
  case 'test-server': runAction(testServer(name),'Could not test server'); break;
  case 'remove-server': runAction(removeServer(name),'Could not remove server'); break;
  case 'toggle-policy-form': togglePolicyForm(); break;
  case 'save-policy': runAction(savePolicy(),'Could not save policy'); break;
  case 'edit-policy': editPolicy(name); break;
  case 'delete-policy': runAction(deletePolicy(name),'Could not delete policy'); break;
  case 'resolve': resolve(el.dataset.kind||'',id); break;
  case 'open-conversation': openConversation(el.dataset.conversationKey||''); break;
  case 'open-session': openSession(id); break;
  case 'open-job': openJobById(id); break;
  case 'set-execution-filter': setExecutionFilter(el.dataset.filter||'all'); break;
  case 'set-view': setTermView(el.dataset.key||'',el.dataset.view||'dialog'); break;
  case 'answer-prompt': sendPromptAnswer(el.dataset.key||'',el.dataset.answer||''); break;
  case 'send-prompt': sendPromptAnswer(el.dataset.key||'',null); break;
  case 'toggle-hold': runAction(toggleHold(id,el.dataset.kind||''),'Could not change takeover mode'); break;
  case 'kill-job': runAction(killJob(id),'Could not stop command'); break;
  }
});
document.getElementById('gate-token').addEventListener('keydown',e=>{ if(e.key==='Enter') submitGate(); });
document.getElementById('hist-filter').addEventListener('input',renderAudit);
document.getElementById('conversation-filter').addEventListener('input',e=>{conversationFilter=e.target.value||'';refreshDialogTree();});
document.addEventListener('focusin',e=>{lastFocusedControl=e.target;});
document.addEventListener('keydown',e=>{
  if(e.key==='Escape'){closeHistory();if(sidebarOpen)setSidebarOpen(false,'toggle');}
  if(e.key==='Enter'&&e.target.classList&&e.target.classList.contains('prompt-input')){e.preventDefault();sendPromptAnswer(e.target.dataset.key||'',null);}
  if(e.target.classList&&e.target.classList.contains('side-tab')&&['ArrowLeft','ArrowRight','Home','End'].includes(e.key)){const tabs=Array.from(document.querySelectorAll('.side-tab')),at=tabs.indexOf(e.target);let next=at;if(e.key==='ArrowLeft')next=(at-1+tabs.length)%tabs.length;if(e.key==='ArrowRight')next=(at+1)%tabs.length;if(e.key==='Home')next=0;if(e.key==='End')next=tabs.length-1;e.preventDefault();setSidebarView(tabs[next].dataset.view,true);}
});

window.addEventListener('resize',()=>{ if(active&&terms[active]&&terms[active].fit){try{terms[active].fit.fit();}catch(_){}}});
document.addEventListener('visibilitychange',()=>{if(document.hidden){clearTimeout(fallbackTimer);return;}scheduleStateRefresh(0);});
if(mobileSidebarQuery.addEventListener)mobileSidebarQuery.addEventListener('change',syncSidebarMode);else mobileSidebarQuery.addListener(syncSidebarMode);
applyLocale();setSidebarView('conversations');syncSidebarMode();
refresh().then(()=>startFeed()).catch(()=>{}); // a missing/invalid token receives 401 and opens the gate
loadPolicies();
