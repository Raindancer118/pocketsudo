package de.raindancer118.pocketdeck;

import android.content.Context;
import android.content.SharedPreferences;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

/** Shortcuts (chains of agent actions) live on the phone; the agent only runs them. */
final class Shortcuts {
    /** What a step needs before it may run. */
    enum Need { NONE, SESSION, FINGER }

    /** Kind of the single parameter a step type takes. */
    enum Param { NONE, NUMBER, TEXT, APP, MEDIA, ONOFF }

    static final class StepType {
        final String type, argKey;
        final int label;
        final Param param;
        final Need need;

        StepType(String type, int label, Param param, String argKey, Need need) {
            this.type = type;
            this.label = label;
            this.param = param;
            this.argKey = argKey;
            this.need = need;
        }
    }

    static final StepType[] CATALOG = {
            new StepType("workspace", R.string.step_workspace, Param.NUMBER, "id", Need.NONE),
            new StepType("app.launch", R.string.step_app, Param.APP, "id", Need.NONE),
            new StepType("media", R.string.step_media, Param.MEDIA, "action", Need.NONE),
            new StepType("volume.set", R.string.step_volume, Param.NUMBER, "percent", Need.NONE),
            new StepType("brightness.set", R.string.step_brightness, Param.NUMBER, "percent", Need.NONE),
            new StepType("open.url", R.string.step_url, Param.TEXT, "url", Need.NONE),
            new StepType("dnd", R.string.step_dnd, Param.ONOFF, "on", Need.NONE),
            new StepType("lock", R.string.step_lock, Param.NONE, null, Need.NONE),
            new StepType("key", R.string.step_key, Param.TEXT, "combo", Need.SESSION),
            new StepType("type", R.string.step_type, Param.TEXT, "text", Need.SESSION),
            new StepType("command", R.string.step_command, Param.TEXT, "cmd", Need.FINGER),
    };

    static StepType find(String type) {
        for (StepType t : CATALOG) if (t.type.equals(type)) return t;
        return null;
    }

    static final class Step {
        String type;
        JSONObject args = new JSONObject();
        /** Human-readable value (app name instead of desktop id). */
        String display = "";
    }

    static final class Shortcut {
        String id = UUID.randomUUID().toString();
        String name = "";
        String icon = "zap";
        final List<Step> steps = new ArrayList<>();

        boolean needsFinger() {
            for (Step s : steps) {
                StepType t = find(s.type);
                if (t != null && t.need != Need.NONE) return true;
            }
            return false;
        }

        /** Body of the agent's "run" action. */
        String chainJson() throws JSONException {
            JSONArray a = new JSONArray();
            for (Step s : steps) a.put(new JSONObject().put("type", s.type).put("args", s.args));
            return new JSONObject().put("name", name).put("steps", a).toString();
        }

        JSONObject toJson() throws JSONException {
            JSONArray a = new JSONArray();
            for (Step s : steps) a.put(new JSONObject().put("type", s.type).put("args", s.args).put("display", s.display));
            return new JSONObject().put("id", id).put("name", name).put("icon", icon).put("steps", a);
        }

        static Shortcut fromJson(JSONObject o) throws JSONException {
            Shortcut sc = new Shortcut();
            sc.id = o.getString("id");
            sc.name = o.optString("name");
            sc.icon = o.optString("icon", "zap");
            JSONArray a = o.optJSONArray("steps");
            for (int i = 0; a != null && i < a.length(); i++) {
                JSONObject so = a.getJSONObject(i);
                Step s = new Step();
                s.type = so.getString("type");
                s.args = so.optJSONObject("args") == null ? new JSONObject() : so.getJSONObject("args");
                s.display = so.optString("display");
                sc.steps.add(s);
            }
            return sc;
        }
    }

    private final SharedPreferences prefs;

    Shortcuts(Context ctx) {
        prefs = ctx.getApplicationContext().getSharedPreferences("deck", Context.MODE_PRIVATE);
    }

    synchronized List<Shortcut> all() {
        List<Shortcut> out = new ArrayList<>();
        try {
            JSONArray a = new JSONArray(prefs.getString("shortcuts", "[]"));
            for (int i = 0; i < a.length(); i++) out.add(Shortcut.fromJson(a.getJSONObject(i)));
        } catch (JSONException ignored) {
        }
        return out;
    }

    synchronized Shortcut get(String id) {
        for (Shortcut s : all()) if (s.id.equals(id)) return s;
        return null;
    }

    synchronized void save(Shortcut sc) {
        List<Shortcut> list = all();
        boolean replaced = false;
        for (int i = 0; i < list.size(); i++) {
            if (list.get(i).id.equals(sc.id)) {
                list.set(i, sc);
                replaced = true;
            }
        }
        if (!replaced) list.add(sc);
        write(list);
    }

    synchronized void delete(String id) {
        List<Shortcut> list = all();
        list.removeIf(s -> s.id.equals(id));
        write(list);
    }

    private void write(List<Shortcut> list) {
        JSONArray a = new JSONArray();
        try {
            for (Shortcut s : list) a.put(s.toJson());
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        prefs.edit().putString("shortcuts", a.toString()).apply();
    }

    // ---- templates ----

    static Step step(String type, String key, Object value, String display) {
        Step s = new Step();
        s.type = type;
        try {
            if (key != null) s.args.put(key, value);
        } catch (JSONException ignored) {
        }
        s.display = display;
        return s;
    }

    static List<Shortcut> templates(Context ctx) {
        List<Shortcut> t = new ArrayList<>();
        Shortcut work = new Shortcut();
        work.name = ctx.getString(R.string.tpl_work);
        work.icon = "code";
        work.steps.add(step("workspace", "id", 3, "3"));
        work.steps.add(step("dnd", "on", true, ctx.getString(R.string.on)));
        work.steps.add(step("volume.set", "percent", 30, "30 %"));
        t.add(work);

        Shortcut off = new Shortcut();
        off.name = ctx.getString(R.string.tpl_evening);
        off.icon = "moon";
        off.steps.add(step("media", "action", "pause", ctx.getString(R.string.media_pause)));
        off.steps.add(step("dnd", "on", false, ctx.getString(R.string.off)));
        off.steps.add(step("lock", null, null, ""));
        t.add(off);

        Shortcut pres = new Shortcut();
        pres.name = ctx.getString(R.string.tpl_present);
        pres.icon = "screen";
        pres.steps.add(step("dnd", "on", true, ctx.getString(R.string.on)));
        pres.steps.add(step("brightness.set", "percent", 100, "100 %"));
        pres.steps.add(step("volume.set", "percent", 60, "60 %"));
        t.add(pres);
        return t;
    }
}
