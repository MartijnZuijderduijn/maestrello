import { api } from "./api.js";
import { closeDrawer, openDrawer } from "./drawer.js";
import { readToolDefaults, settingsFields, toolDefaultsGrid } from "./forms.js";
import { harnessAvailability, harnessInfo, modelSelect } from "./harness.js";
import { pathField } from "./picker.js";
import { cards, setOpenId } from "./state.js";
import { projects } from "./projects.js";
import { $, esc, select } from "./util.js";

export async function openSettings() {
  setOpenId("settings");
  const s = await api("GET", "/api/settings");
  openDrawer("Settings", `<label>Default root folder</label>${pathField("s-root", s.default_root_folder)}
    ${settingsFields({ harness: s.harness, model: s.default_model, effort: s.default_effort, permission_mode: s.default_permission_mode,
      max_turns: s.default_max_turns, max_minutes: s.default_max_minutes, push_default: s.default_push_default })}
    <label class="check"><input type="checkbox" id="s-autorun" ${s.default_auto_run ? "checked" : ""}> Auto-run after refinement by default</label>
    <label class="check"><input type="checkbox" id="s-skip" ${s.default_skip_refinement ? "checked" : ""}> Skip refinement by default (new cards go straight to Ready with their own text as the prompt)</label>
    <label class="check"><input type="checkbox" id="s-automodel" ${s.auto_model ? "checked" : ""}> Automatic model selection by default for new cards (each card can switch it: the refiner picks the smallest capable model and adapter, and the manual model is only the fallback)</label>
    <label>Model capability and cost (JSON, <code>"harness/model": {"tier": 1-5, "cost": relative, 0 = free}</code>; adds to the built-in claude defaults)</label>
    <textarea id="s-modelinfo" rows="4">${esc(JSON.stringify(s.model_info || {}, null, 1))}</textarea>
    <div class="row">
      <div><label>Refinement model</label>${modelSelect("s-rmodel", s.refine_model, harnessInfo(s.harness))}</div>
      <div><label>Refinement effort</label>${select("s-reffort", harnessInfo(s.harness).efforts, s.refine_effort)}</div>
    </div>
    ${toolDefaultsGrid(s.default_tools || {})}
    <label>Overrides per card kind (JSON, <code>"research": {"harness": "opencode", "model": "…"}</code>; settings left out fall through to the values above)</label>
    <textarea id="s-kinds" rows="4">${esc(JSON.stringify(s.kinds || {}, null, 1))}</textarea>
    <label>Project settings (JSON overrides for one project, with its own <code>"kinds"</code>; most specific wins: kind in project, project, kind, global)</label>
    <div class="row">
      <div><select id="s-proj" onchange="projectSettingsChanged()">${[...projects.values()].map((p) => `<option>${esc(p.name)}</option>`).join("")}</select></div>
      <div><button onclick="saveProjectSettings()">Save project settings</button></div>
    </div>
    <textarea id="s-projsettings" rows="5">${esc(JSON.stringify([...projects.values()][0]?.settings || {}, null, 1))}</textarea>
    ${harnessAvailability()}
    <div class="actions"><button class="primary" onclick="saveSettings()">Save</button></div>`);
}
const projByName = () => [...projects.values()].find((p) => p.name === $("#s-proj")?.value);
export function projectSettingsChanged() {
  $("#s-projsettings").value = JSON.stringify(projByName()?.settings || {}, null, 1);
}
export async function saveProjectSettings() {
  try {
    const p = projByName();
    if (!p) throw new Error("No project selected");
    let settings;
    try { settings = JSON.parse($("#s-projsettings").value.trim() || "{}"); } catch (e) { throw new Error("Project settings is not valid JSON"); }
    projects.set(p.id, await api("PATCH", "/api/projects/" + p.id, { settings }));
    alert("Saved settings of project " + p.name);
  } catch (e) { alert(e.message); }
}
export async function saveSettings() {
  try {
    let kinds;
    try { kinds = JSON.parse($("#s-kinds").value.trim() || "{}"); } catch (e) { throw new Error("Overrides per card kind is not valid JSON"); }
    let modelInfo;
    try { modelInfo = JSON.parse($("#s-modelinfo").value.trim() || "{}"); } catch (e) { throw new Error("Model capability and cost is not valid JSON"); }
    await api("PUT", "/api/settings", {
      auto_model: $("#s-automodel").checked, model_info: modelInfo, kinds,
      default_root_folder: $("#s-root").value, default_model: $("#f-model").value, default_effort: $("#f-effort").value,
      default_permission_mode: $("#f-mode").value, default_max_turns: +$("#f-turns").value, default_max_minutes: +$("#f-minutes").value, default_push_default: $("#f-push").value,
      refine_model: $("#s-rmodel").value, refine_effort: $("#s-reffort").value, default_auto_run: $("#s-autorun").checked,
      default_skip_refinement: $("#s-skip").checked, default_tools: readToolDefaults(),
      harness: $("#f-harness").value,
    });
    closeDrawer();
  } catch (e) { alert(e.message); }
}
