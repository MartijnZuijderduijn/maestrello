import { loadAcct, refreshAcct, toggleAcct } from "./account.js";
import { act, api, connect, loadCards } from "./api.js";
import { blockDrag, cardClick, cardKey, clearDone, deleteDoneCard, dragEnd, dragLeave, dragOver, dragStart, dropCard, refineAllTodo, showBoardError, showBoardSkeleton, startAllReady } from "./board.js";
import { openCard } from "./card.js";
import { closeDrawer } from "./drawer.js";
import { addContextPick, addContextTyped, createCard, kindChanged, newPromptKey, openNew, projectChanged, removeContext, setToolbox, skipChanged } from "./forms.js";
import { autoModelChanged, harnessChanged, recheckHarnesses, setHarnesses, setModels } from "./harness.js";
import { createProject, loadSel, openEditProject, openNewProject, saveProject, setSel, toggleLane, toggleProj, toggleProject } from "./projects.js";
import { answer, approve, chatKey, deleteCard, discard, planSelection, replyChat, saveAgent, saveCard, saveReady, sendBack } from "./review.js";
import { browsePath, pickPath } from "./picker.js";
import { openSettings, projectSettingsChanged, saveProjectSettings, saveSettings, settingsRunFlagsChanged } from "./settings.js";
import { $ } from "./util.js";

// The markup uses inline on*= handlers, which look their functions up on window.
Object.assign(window, { settingsRunFlagsChanged, refineAllTodo, autoModelChanged, act, addContextPick, addContextTyped, removeContext, answer, approve, blockDrag, browsePath, cardClick, cardKey, chatKey, clearDone, closeDrawer, createCard, createProject, deleteCard, deleteDoneCard, discard, dragEnd, dragLeave, dragOver, dragStart, dropCard, harnessChanged, kindChanged, newPromptKey, openCard, openNew, openNewProject, openEditProject, saveProject, openSettings, pickPath, planSelection, projectChanged, projectSettingsChanged, saveProjectSettings, recheckHarnesses, refreshAcct, replyChat, saveAgent, saveCard, saveReady, saveSettings, sendBack, setSel, skipChanged, startAllReady, toggleAcct, toggleLane, toggleProj, toggleProject });

showBoardSkeleton();
(async () => {
  try {
    const info = await api("GET", "/api/info");
    if (info.sandbox_root) { const s = $("#sandbox"); s.hidden = false; s.textContent = "sandbox: " + info.sandbox_root; }
    loadSel();
    loadAcct();
    try { const m = await api("GET", "/api/models"); if (m.length) setModels(m); } catch (e) {}
    try { const h = await api("GET", "/api/harnesses"); if (h.length) setHarnesses(h); } catch (e) {}
    try { setToolbox(await api("GET", "/api/toolbox")); } catch (e) {}
    await loadCards(); connect();
  } catch (e) { showBoardError(e.message); } // never leave the skeleton shimmering
})();
