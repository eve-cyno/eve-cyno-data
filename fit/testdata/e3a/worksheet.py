#!/usr/bin/env python3
"""E3a worksheet (T4.3): reference numbers for the 16 profile-fit pairs.

A complete second dogma interpreter (Item, Fit.targets, val, chain) written from SDE values of the
pinned build 3670638_20261104 and public rules. It is an independent re-implementation, not hand
arithmetic: E3a is a differential test against it. Capacitor and stacking formulas cite EVE University
pages. It imports no engine code and no Pyfa/eos material, and no output of any of them was used. Usage: python3 worksheet.py <sde.sqlite>
It reads profiles.json and ref1..ref6.eft from its own folder and rewrites cases.json and worksheet.md.
"""
"""E3a worksheet: independent re-implementation of the dogma rules over SDE values. No engine code."""
import json, math, re, sys, sqlite3, os
HERE = os.path.dirname(os.path.abspath(__file__))
SDE = sys.argv[1] if len(sys.argv) > 1 else os.environ['EVE_CORE_SDE_PATH']
c = sqlite3.connect('file:%s?mode=ro' % SDE, uri=True)
NAME = {}; BYNAME = {}; GROUP = {}; CAT = {}
for t, n, g in c.execute('select typeID,typeName,groupID from invTypes'):
    NAME[t] = n; BYNAME.setdefault(n, t); GROUP[t] = g
AN = {a: n for a, n in c.execute('select attributeID,attributeName from dgmAttributeTypes')}
STACKABLE = {a: s for a, s in c.execute('select attributeID,stackable from dgmAttributeTypes')}
DEFAULT = {a: d for a, d in c.execute('select attributeID,defaultValue from dgmAttributeTypes')}
VOLUME = {t: v for t, v in c.execute('select typeID,volume from invTypes')}
MASS = {t: v for t, v in c.execute('select typeID,mass from invTypes')}
_attrs = {}
def attrs(t):
    if t not in _attrs:
        d = {}
        for a, i, f in c.execute('select attributeID,valueInt,valueFloat from dgmTypeAttributes where typeID=?', (t,)):
            d[a] = float(f if f is not None else i)
        _attrs[t] = d
    return _attrs[t]
_eff = {}
def effects(t):
    if t not in _eff:
        out = []
        for eid, en, cat, mi in c.execute('select e.effectID,e.effectName,e.effectCategory,e.modifierInfo from dgmEffects e join dgmTypeEffects x on x.effectID=e.effectID where x.typeID=?', (t,)):
            mods = json.loads(mi) if mi else None
            out.append((eid, en, int(cat), mods))
        _eff[t] = out
    return _eff[t]
RS = [182, 183, 184, 1285, 1289, 1290]
def reqskills(t):
    a = attrs(t); return {int(a[s]) for s in RS if a.get(s)}

class Item:
    def __init__(s, tid, kind, loc, state='active', host=None):
        s.tid = tid; s.kind = kind; s.loc = loc; s.state = state; s.host = host
        s.base = dict(attrs(tid))
        if 4 not in s.base and MASS.get(tid): s.base[4] = float(MASS[tid])
        s.group = GROUP.get(tid); s.req = reqskills(tid); s.charge = None
    def __repr__(s): return '%s(%s)' % (s.kind, NAME.get(s.tid))

def parse_eft(path):
    """-> hull name, list of (slot, module name, charge name or None, qty)"""
    lines = [l.rstrip() for l in open(path)]
    mods = []; hull = None; sec = 0
    for l in lines:
        if l.startswith('//'): continue
        if l.startswith('['):
            hull = l[1:].split(',')[0]; continue
        if not l.strip(): sec += 1; continue
        mods.append((sec, l.strip()))
    return hull, mods

def build(hull, eft_mods, skills):
    ship = Item(BYNAME[hull], 'ship', 'space')
    char = Item(1373, 'char', 'none')
    items = [ship, char]
    for sid, lvl in skills.items():
        it = Item(int(sid), 'skill', 'char'); it.base = dict(it.base); it.base[280] = float(lvl); items.append(it)
    return ship, char, items

CATEGORY = {t: g for t, g in c.execute('select typeID,groupID from invTypes')}
GCAT = {g: cat for g, cat in c.execute('select groupID,categoryID from invGroups')}
def slot_of(t):
    es = {e[1] for e in effects(t)}
    if 'loPower' in es: return 'low'
    if 'medPower' in es: return 'mid'
    if 'hiPower' in es: return 'high'
    if 'rigSlot' in es: return 'rig'
    if 'subSystem' in es: return 'sub'
    if GCAT.get(GROUP[t]) == 18: return 'drone'
    return None

class Fit:
    def __init__(s, eftpath, skills):
        hull, lines = parse_eft(eftpath)
        s.ship = Item(BYNAME[hull], 'ship', 'space'); s.char = Item(1373, 'char', 'none')
        s.items = [s.ship, s.char]; s.modules = []; s.drones = []; s.skills = {}
        for sid, lvl in skills.items():
            it = Item(int(sid), 'skill', 'char'); it.base = dict(it.base); it.base[280] = float(lvl)
            s.items.append(it); s.skills[int(sid)] = it
        for _, l in lines:
            m = re.match(r'^(.*) x(\d+)$', l)
            qty = 1
            if m: l, qty = m.group(1), int(m.group(2))
            parts = [p.strip() for p in l.split(',')]
            # module names may contain a comma-free text; charge is after the first comma
            name = parts[0]; ch = parts[1] if len(parts) > 1 else None
            t = BYNAME[name]; sl = slot_of(t)
            for _ in range(qty):
                if sl == 'drone':
                    d = Item(t, 'drone', 'space'); s.drones.append(d); s.items.append(d)
                else:
                    kind = {'rig': 'rig', 'sub': 'sub'}.get(sl, 'module')
                    it = Item(t, kind, 'ship'); it.slot = sl; s.modules.append(it); s.items.append(it)
                    if ch:
                        cc = Item(BYNAME[ch], 'charge', 'ship', host=it); it.charge = cc; s.items.append(cc)
        s._mods = None; s._memo = {}
    # --- modifier collection ---
    def active_effects(s, it):
        out = []
        for eid, en, cat, mods in effects(it.tid):
            if cat == 0: ok = True
            elif cat == 4: ok = it.kind in ('module', 'rig', 'sub', 'charge', 'drone', 'ship')
            elif cat == 1: ok = it.kind in ('module',) and it.state == 'active'
            else: ok = False
            if ok: out.append((eid, en, mods))
        return out
    def all_mods(s):
        if s._mods is None:
            L = []
            # effects with no modifier data in the SDE, applied by hand from the skill descriptions
            for it in s.items:
                if it.kind != 'skill': continue
                for eid, en, cat, mods in effects(it.tid):
                    if mods is not None: continue
                    S = it.tid
                    if en == 'droneDmgBonus':
                        L.append((it, eid, en, dict(func='Special', kind='drone', skillTypeID=S, modifiedAttributeID=64, modifyingAttributeID=292, operation=6)))
                    elif en in ('missileEMDmgBonus', 'missileExplosiveDmgBonus', 'missileThermalDmgBonus', 'missileKineticDmgBonus2'):
                        a = {'missileEMDmgBonus': 114, 'missileExplosiveDmgBonus': 116, 'missileThermalDmgBonus': 118, 'missileKineticDmgBonus2': 117}[en]
                        L.append((it, eid, en, dict(func='Special', kind='charge', skillTypeID=S, modifiedAttributeID=a, modifyingAttributeID=292, operation=6)))
                    elif en == 'selfRof':
                        L.append((it, eid, en, dict(func='Special', kind='module', skillTypeID=S, modifiedAttributeID=51, modifyingAttributeID=293, operation=6)))
            # Reactive Armor Hardener (effect 4928, no modifierInfo): its own four armor resonances (attributes
            # 267-270, default 0.85) multiply the ship's, PreMul so it stacks with the Damage Control only (EVE
            # University wiki, "Armor tanking"). The adaptive shift is not modelled.
            for it in s.items:
                if it.kind == 'module' and it.state == 'active' and any(e[0] == 4928 for e in effects(it.tid)):
                    for a in (267, 268, 269, 270):
                        L.append((it, 4928, 'adaptiveArmorHardener', dict(func='ItemModifier', domain='shipID', modifiedAttributeID=a, modifyingAttributeID=a, operation=0)))
            for it in s.items:
                for eid, en, mods in s.active_effects(it):
                    if en == 'skillEffect': continue  # skill level is the trained level; this effect only models training state
                    for m in mods or []:
                        L.append((it, eid, en, m))
            s._mods = L
        return s._mods
    def targets(s, src, m):
        """items matched by modifier m of source src"""
        fn = m['func']
        if fn == 'Special': return [i for i in s.items if i.kind == m['kind'] and m['skillTypeID'] in i.req]
        dom = m['domain']
        if fn == 'EffectStopper': return []
        if dom == 'shipID': loc = 'ship'; one = s.ship
        elif dom == 'charID': loc = 'char'; one = s.char
        elif dom == 'itemID': loc = None; one = src
        elif dom == 'otherID':
            one = src.charge if src.kind != 'charge' else src.host; loc = None
        else: return []
        if fn == 'ItemModifier': return [one] if one is not None else []
        if fn == 'OwnerRequiredSkillModifier':
            return [i for i in s.items if i.kind != 'char' and m['skillTypeID'] in i.req]
        pool = [i for i in s.items if i.loc == loc]
        if dom == 'shipID' : pool = [i for i in s.items if i.loc == 'ship']
        if fn == 'LocationRequiredSkillModifier': return [i for i in pool if m['skillTypeID'] in i.req]
        if fn == 'LocationGroupModifier': return [i for i in pool if i.group == m['groupID']]
        if fn == 'LocationModifier': return pool
        return []
    def val(s, it, a, trace=None):
        key = (id(it), a)
        if key in s._memo and trace is None: return s._memo[key]
        base = it.base.get(a, DEFAULT.get(a) or 0.0)
        pre_assign = []; pre = []; add = []; post = []; post_assign = []
        for src, eid, en, m in s.all_mods():
            if m['modifiedAttributeID'] != a: continue
            if it not in s.targets(src, m): continue
            v = s.val(src, m['modifyingAttributeID'])
            if eid == 899 and src.tid == 11993: v *= 2  # Cerberus: 5 % kinetic per level, the SDE row (2.5) is shared with EM, thermal, explosive (EVE University wiki "Cerberus")
            op = m['operation']
            pen = src.kind in ('module', 'rig', 'charge') and not STACKABLE.get(a, 1)
            rec = (src, en, v, pen)
            if op == -1: pre_assign.append(rec)
            elif op == 0: pre.append((rec, v))
            elif op == 1: pre.append((rec, 1.0 / v if v else 1.0))
            elif op == 2: add.append(v)
            elif op == 3: add.append(-v)
            elif op == 4: post.append((rec, v))
            elif op == 5: post.append((rec, 1.0 / v if v else 1.0))
            elif op == 6: post.append((rec, 1 + v / 100.0))
            elif op == 7: post_assign.append(v)
            elif op == 9: pass
        x = base
        if pre_assign: x = pre_assign[-1][2]
        x = s.chain(x, pre) ; x += sum(add); x = s.chain(x, post)
        if post_assign: x = post_assign[-1]
        if trace is None: s._memo[key] = x
        return x
    @staticmethod
    def chain(x, lst):
        pen = [m for (rec, m) in lst if rec[3]]
        for rec, m in lst:
            if not rec[3]: x *= m
        for grp in ([m for m in pen if m >= 1], [m for m in pen if m < 1]):
            grp = sorted(grp, key=lambda m: -abs(m - 1))
            for i, m in enumerate(grp):
                x *= 1 + (m - 1) * math.exp(-((i / 2.67) ** 2))
        return x

def load_profiles():
    P = json.load(open(os.path.join(HERE, 'profiles.json')))['profiles']
    return {k: {int(a): b for a, b in v['skills'].items()} for k, v in P.items()}
FIT = os.path.join(HERE, 'ref%d.eft')
def allv():
    P = load_profiles(); return {k: 5 for k in P['all0']}

RES4_SHIELD = (271, 272, 273, 274); RES4_ARMOR = (267, 268, 269, 270); RES4_HULL = (109, 110, 111, 113)
PAIRS = [  # (hull ref, profile)
 (1, 'all0'), (1, 'allI'), (1, 'allIII'), (1, 'alpha'),
 (2, 'all0'), (2, 'droner'), (2, 'alpha'),
 (3, 'missiler'), (3, 'fitIII'), (4, 'fitIII'),
 (5, 'allIII'), (5, 'alpha'), (6, 'tanker'), (6, 'droner'), (3, 'allI'), (5, 'allI')]

def cap_simulate(C, T, evs):
    """Discrete model: every module fires at t = 0 and at each cycle start, takes its full cost up front, a booster charge
    injects right after, recharge between events C' = Cmax*(1+(sqrt(C/Cmax)-1)*exp(-5 dt/T))^2 (EVE University wiki,
    "Capacitor"); the run ends at the first activation the capacitor cannot pay. evs = [(need, inject, cycle_s)]."""
    n = [0] * len(evs); c = C; t = 0.0
    for _ in range(2000000):
        nxt = min(n[i] * evs[i][2] for i in range(len(evs)))
        if nxt > t:
            r = 1 + (math.sqrt(c / C) - 1) * math.exp(-5 * (nxt - t) / T); c = C * r * r; t = nxt
        for i, (need, inj, cy) in enumerate(evs):
            if n[i] * cy == nxt:
                if c < need: return t
                c = min(C, c - need + inj); n[i] += 1
    return None

def cap_equilibrium(C, T, use, inj, evs):
    peak = 2.5 * C / T
    net = use - inj
    if net <= 0: return True, 100.0, 0.0
    if net <= peak:
        k = net / peak
        return True, ((1 + math.sqrt(1 - k)) / 2) ** 2 * 100.0, 0.0
    # unstable (k > 1): the game's discrete simulation. The continuous closed form (substitute s = sqrt(c), dc/dt = a (s - c) - d with
    # a = 10/T, d = net/C: t = (2/a) atan(1/(2w))/w, w = sqrt(q - 1/4), q = d/a) is the upper bound it stays below.
    a = 10.0 / T; q = net / C / a; w = math.sqrt(q - 0.25)
    t_closed = 2.0 / a * math.atan(1.0 / (2.0 * w)) / w
    t = cap_simulate(C, T, evs)
    if t is None: return False, 0.0, t_closed
    assert t <= t_closed + 1e-6, (t, t_closed)
    return False, 0.0, t

def numbers(f):
    sh, ch = f.ship, f.char
    n = {}
    mods = [m for m in f.modules if m.kind in ('module', 'sub')]
    n['cpu_cap'] = f.val(sh, 48); n['cpu_used'] = sum(f.val(m, 50) for m in f.modules)
    n['pg_cap'] = f.val(sh, 11); n['pg_used'] = sum(f.val(m, 30) for m in f.modules)
    n['calib_cap'] = f.val(sh, 1132); n['calib_used'] = sum(f.val(m, 1153) for m in f.modules if m.kind == 'rig')
    # drones
    bw = f.val(sh, 1271); mx = int(round(f.val(ch, 352)))
    used = 0.0; launched = []
    for d in f.drones:
        b = f.val(d, 1272)
        if len(launched) < mx and used + b <= bw + 1e-9:
            launched.append(d); used += b
    n['drone_bw_cap'] = bw; n['drone_bw_used'] = used
    n['drone_bay_cap'] = f.val(sh, 283); n['drone_bay_used'] = sum(VOLUME[d.tid] for d in f.drones)
    n['drone_max_active'] = mx; n['drone_active'] = len(launched)
    n['max_locked'] = min(f.val(sh, 192), f.val(ch, 192))
    # dps
    wd = 0.0
    for m in mods:
        if m.charge is None: continue
        sp = f.val(m, 51)
        if sp <= 0: continue
        dmg = sum(f.val(m.charge, a) for a in (114, 116, 117, 118))
        mult = f.val(m, 64) if 64 in m.base else f.val(ch, 212)  # turret: damageMultiplier; launcher: pilot missileDamageMultiplier
        wd += dmg * mult / (sp / 1000.0)
    dd = 0.0
    for d in launched:
        dmg = sum(f.val(d, a) for a in (114, 116, 117, 118)); sp = f.val(d, 51)
        dd += dmg * f.val(d, 64) / (sp / 1000.0)
    n['dps_weapons'] = wd; n['dps_drones'] = dd; n['dps_total'] = wd + dd
    # ehp, uniform
    def layer(hp, ids): return f.val(sh, hp) / (sum(f.val(sh, i) for i in ids) / 4.0)
    n['ehp'] = layer(263, RES4_SHIELD) + layer(265, RES4_ARMOR) + layer(9, RES4_HULL)
    # capacitor
    C = f.val(sh, 482); T = f.val(sh, 55) / 1000.0
    use = inj = 0.0; evs = []
    # activation order at the same instant: high, mid, low, rig, subsystem slots (the engine's item order); it matters only through a
    # booster charge that overfills the capacitor
    SLOT_RANK = {'high': 0, 'mid': 1, 'low': 2}
    for m in sorted(mods, key=lambda m: SLOT_RANK.get(getattr(m, 'slot', None), 3)):
        need = f.val(m, 6); dur = f.val(m, 73) or f.val(m, 51)
        if dur <= 0: continue
        ev_need = need if (need > 0 and m.state == 'active') else 0.0
        ev_inj = f.val(m.charge, 67) if (m.charge is not None and f.val(m.charge, 67) > 0) else 0.0
        use += ev_need / (dur / 1000.0); inj += ev_inj / (dur / 1000.0)
        if ev_need > 0 or ev_inj > 0: evs.append((ev_need, ev_inj, dur / 1000.0))
    st, pct, tte = cap_equilibrium(C, T, use, inj, evs)
    n.update(cap_capacity=C, cap_recharge_s=T, cap_peak=2.5 * C / T, cap_use=use, cap_inject=inj, cap_stable=st, cap_stable_pct=pct, cap_seconds_to_empty=tte)
    v = f.val(sh, 37)
    props = [m for m in mods if m.state == 'active' and any(e[0] in (6730, 6731) for e in effects(m.tid))]
    mass = f.val(sh, 4) + sum(f.val(m, 796) for m in props)
    n['speed_passive'] = v; n['mass_total'] = mass
    for m in props:
        v *= 1 + f.val(m, 20) / 100.0 * f.val(m, 567) / mass
    n['speed'] = v
    return n


import json, sys, datetime

OPN = {-1: 'assign', 0: 'x', 1: '/', 2: '+', 3: '-', 4: 'x', 5: '/', 6: '%', 7: 'assign'}
def modlist(f, it, a):
    out = []
    for src, eid, en, m in f.all_mods():
        if m['modifiedAttributeID'] == a and it in f.targets(src, m):
            v = f.val(src, m['modifyingAttributeID'])
            pen = src.kind in ('module', 'rig', 'charge') and not STACKABLE.get(a, 1) and m['operation'] in (0, 1, 4, 5, 6)
            out.append((m['operation'], src, v, pen))
    return out
def ex(f, it, a, nm=None):
    base = it.base.get(a, DEFAULT.get(a) or 0.0)
    parts = []
    for op, src, v, pen in sorted(modlist(f, it, a), key=lambda x: x[0]):
        sym = OPN[op]; nmz = NAME[src.tid]
        if op == 6: parts.append('%s %+g%%%s' % (nmz, v, ' (stack-penalised)' if pen else ''))
        elif op in (0, 4): parts.append('x%g %s%s' % (v, nmz, ' (stack-penalised)' if pen else ''))
        elif op in (1, 5): parts.append('/%g %s' % (v, nmz))
        elif op == 2: parts.append('+%g %s' % (v, nmz))
        elif op == 3: parts.append('-%g %s' % (v, nmz))
        else: parts.append('=%g %s' % (v, nmz))
    s = 'attr %d %s: base %g' % (a, AN[a], base)
    if parts: s += '; ' + '; '.join(parts)
    return s + ' => %.10g' % f.val(it, a)
def grp(f, mods, a, label):
    """sum of attribute over modules, one line per distinct module type"""
    seen = {}; lines = []
    for m in mods:
        key = m.tid
        seen.setdefault(key, []).append(f.val(m, a))
    tot = 0.0
    for t, vs in seen.items():
        tot += sum(vs)
        mm = [m for m in mods if m.tid == t][0]
        d = ex(f, mm, a)
        lines.append('%dx %s (%s) = %.10g' % (len(vs), NAME[t], d, vs[0]))
    return lines, tot

SRC = {
 'res': 'EVE University "Fitting" (CPU, powergrid, calibration); SDE attrs 48/50 (CPU), 11/30 (PG), 1132/1153 (calibration); skill bonuses per skill description and its SDE bonus attribute',
 'drone': 'EVE University "Drones"; SDE attrs 1271 (bandwidth), 1272 (bandwidth used), 283 (bay), 161 (volume), 352 (max active; Drones skill attr 353 per level)',
 'lock': 'EVE University "Targeting"; locked targets = min(ship attr 192, pilot attr 192 = 2 + Target Management + Advanced Target Management, skill attr 311)',
 'dps': 'EVE University "Turret mechanics" and "Missile mechanics"; SDE attrs 114/116/117/118 (damage), 64 (multiplier), 51 (cycle ms), 212 (pilot missile damage multiplier, raised by Ballistic Control System attr 213)',
 'ehp': 'EVE University "Effective HP"; SDE attrs 263/265/9 (HP), resonances 271-274, 267-270, 109/110/111/113; uniform profile',
 'cap': 'EVE University "Capacitor" (page read 2026-11-09): dC/dt = 10*Cmax/T*(sqrt(C/Cmax)-C/Cmax), peak 2.5*Cmax/T, equilibrium fraction ((1+sqrt(1-D/peak))/2)^2; SDE attrs 482, 55, 6, 73/51, 67',
 'speed': 'EVE University "Stacking penalties" (page read 2026-11-09); propulsion boost = speedFactor(20)/100 x thrust(567)/total mass (SDE attr 567 description: "Used to divide with mass"); mass = hull mass + massAddition(796) of plates and the propulsion module',
}
def rel(): return {'kind': 'rel', 'v': 1e-6}
def build_case(ref, pid):
    P = load_profiles(); f = Fit(FIT % ref, P[pid]); n = numbers(f); sh = f.ship; ch = f.char
    mods = [m for m in f.modules if m.kind in ('module', 'sub')]
    cases = {}; doc = []
    def add(key, val, tol, src, formula, est=None, finding=None):
        e = {'value': float(('%.10g' % val)) if not isinstance(val, bool) else (1.0 if val else 0.0), 'tol': tol, 'source': src, 'formula': formula}
        if est: e['estimated'] = est
        if finding: e['finding'] = finding
        cases[key] = e; doc.append('- `%s` = %.10g  (%s)  \n  %s' % (key, e['value'], tol, formula))
    ABS = 'abs0.01'; REL = 'rel1e-6'; EX = 'exact'
    l, t = grp(f, f.modules, 50, 'cpu'); add('cpu_used', n['cpu_used'], ABS, SRC['res'], 'sum of module CPU: ' + ' | '.join(l))
    add('cpu_cap', n['cpu_cap'], ABS, SRC['res'], ex(f, sh, 48))
    l, t = grp(f, f.modules, 30, 'pg'); add('pg_used', n['pg_used'], ABS, SRC['res'], 'sum of module PG: ' + ' | '.join(l))
    add('pg_cap', n['pg_cap'], ABS, SRC['res'], ex(f, sh, 11))
    rigs = [m for m in f.modules if m.kind == 'rig']
    add('calib_used', n['calib_used'], ABS, SRC['res'], 'sum of rig upgradeCost (attr 1153): %s' % ' + '.join('%g' % f.val(m, 1153) for m in rigs))
    add('calib_cap', n['calib_cap'], ABS, SRC['res'], ex(f, sh, 1132))
    add('drone_bw_cap', n['drone_bw_cap'], ABS, SRC['drone'], ex(f, sh, 1271))
    add('drone_bw_used', n['drone_bw_used'], ABS, SRC['drone'], '%d launched drones x attr 1272 (drones launch in fit order while bandwidth and the active cap allow)' % n['drone_active'])
    add('drone_bay_cap', n['drone_bay_cap'], ABS, SRC['drone'], ex(f, sh, 283))
    add('drone_bay_used', n['drone_bay_used'], ABS, SRC['drone'], 'sum of drone volumes (invTypes.volume) of the %d drones in the bay' % len(f.drones))
    add('drone_max_active', n['drone_max_active'], EX, SRC['drone'], ex(f, ch, 352) + ' (pilot; Drones level x 1)')
    add('drone_active', n['drone_active'], EX, SRC['drone'], 'min(max active %d, drones that fit in bandwidth %g)' % (n['drone_max_active'], n['drone_bw_cap']))
    add('max_locked', n['max_locked'], EX, SRC['lock'], 'min(ship %g, pilot %g)' % (f.val(sh, 192), f.val(ch, 192)))
    # dps
    wl = []
    for m in mods:
        if m.charge is None: continue
        sp = f.val(m, 51); dmg = sum(f.val(m.charge, a) for a in (114, 116, 117, 118))
        mult = f.val(m, 64) if 64 in m.base else f.val(f.char, 212)
        wl.append('%s: (%s damage %.10g) x multiplier %.10g / (cycle %.10g ms/1000)' % (NAME[m.tid], NAME[m.charge.tid], dmg, mult, sp))
    add('dps_weapons', n['dps_weapons'], REL, SRC['dps'], ' ; '.join(sorted(set(wl))) + ' ; summed over %d weapons' % len([m for m in mods if m.charge]))
    dl = []
    for d in f.drones[:1]:
        dl.append('%s: damage %.10g x %.10g / (%.10g ms/1000)' % (NAME[d.tid], sum(f.val(d, a) for a in (114, 116, 117, 118)), f.val(d, 64), f.val(d, 51)))
    add('dps_drones', n['dps_drones'], REL, SRC['dps'], 'first bay drone, ' + ' ; '.join(dl) + ' ; summed over the %d launched drones' % n['drone_active'])
    add('dps_total', n['dps_total'], REL, SRC['dps'], 'weapons + drones')
    add('ehp', n['ehp'], REL, SRC['ehp'], 'shield %g/mean(%s) + armor %g/mean(%s) + hull %g/mean(%s)' % (
        f.val(sh, 263), ','.join('%.6g' % f.val(sh, i) for i in RES4_SHIELD), f.val(sh, 265), ','.join('%.6g' % f.val(sh, i) for i in RES4_ARMOR), f.val(sh, 9), ','.join('%.6g' % f.val(sh, i) for i in RES4_HULL)))
    boost = ref in (2, 6)
    est = 'cap booster charge count and reload (10 s) not modelled: injection taken as continuous charge/cycle' if boost else None
    add('cap_capacity', n['cap_capacity'], REL, SRC['cap'], ex(f, sh, 482))
    add('cap_recharge_s', n['cap_recharge_s'], REL, SRC['cap'], ex(f, sh, 55) + ' (ms)')
    cf = 'use %.8g GJ/s (sum need/cycle), injection %.8g GJ/s, peak 2.5*%.8g/%.8g = %.8g GJ/s; stable iff peak + injection >= use' % (n['cap_use'], n['cap_inject'], n['cap_capacity'], n['cap_recharge_s'], n['cap_peak'])
    add('cap_stable', n['cap_stable'], EX, SRC['cap'], cf, est)
    net = n['cap_use'] - n['cap_inject']
    if n['cap_stable']:
        add('cap_stable_pct', n['cap_stable_pct'], REL, SRC['cap'], 'net drain %.8g; k = net/peak = %.8g; pct = 100*((1+sqrt(1-k))/2)^2' % (net, net / n['cap_peak'] if net > 0 else 0), est)
    else:
        add('cap_seconds_to_empty', n['cap_seconds_to_empty'], REL, SRC['cap'], 'discrete activations from t = 0 (stop at the first unpayable one), recharge C\' = Cmax (1 + (sqrt(C/Cmax) - 1) exp(-5 dt/T))^2; the continuous closed form is an upper bound; net drain %.8g GJ/s' % net, est)
    add('speed', n['speed'], REL, SRC['speed'], 'passive max velocity %.10g x (1 + speedFactor/100 x thrust/mass) for each active propulsion module, mass %.10g; no propulsion module: passive value' % (n['speed_passive'], n['mass_total']))
    return cases, doc

if __name__ == '__main__':
    out = {'build': '3670638_20261104', 'method': 'worksheet.py: an independent second dogma interpreter (Item, Fit.targets, val, chain) written from SDE values and public rules, so E3a is a differential test against an independent re-implementation, not hand arithmetic. Capacitor and stacking formulas cite EVE University pages (Capacitor, Stacking penalties). No engine, Pyfa or eos code or output was used. Tolerances: resources 0.01 absolute (plan), counts and flags exact, the rest 1e-6 relative (stricter than the plan 0.1 %).', 'pairs': []}
    md = ['# E3a worksheet (T4.3)', '', 'Pinned SDE build 3670638_20261104. Generated by `worksheet.py` in this folder (`python3 worksheet.py <sde.sqlite>`), run on 2026-11-09. An independent re-implementation of the dogma rules (a second interpreter) over SDE base values; it imports no engine code and no Pyfa/eos material.', '',
          '## Conventions', '',
          '- Modifier data: every skill, hull, module and rig bonus is read from the SDE (`dgmEffects.modifierInfo`, bonus attribute per level scaled by the skill level through the skill\'s own PreMul effect). Hull bonuses were cross-read against `invTraits`, skill bonuses against the skill description.',
          '- Operation order: PreAssign, PreMul/PreDiv, ModAdd/ModSub, PostMul/PostDiv/PostPercent, PostAssign (SDE operation ids -1..7).',
          '- Stacking: EVE University "Stacking penalties": S(n) = exp(-(n/2.67)^2) for the n-th (from 0) modifier, strongest first, positive and negative separately; applied to module, rig and charge sources on attributes with stackable = 0; skills, hull and subsystem bonuses are exempt.',
          '- Effects the SDE carries no modifier data for are applied by hand from the skill descriptions: `droneDmgBonus` (drone damage, attr 292 per level for drones requiring the skill), `missile*DmgBonus` (missile damage, same attribute), `selfRof` (rate of fire, attr 293). Propulsion modules (effects 6730/6731): speed x (1 + speedFactor/100 x thrust/mass).',
          '- States: every module active (EFT default). Skills not named in a profile are the profile level; a unusable module is still evaluated.',
          '- Missile damage: sum of the four damage attributes x pilot missileDamageMultiplier (attr 212, raised by Ballistic Control System attr 213, stacking penalised) / launcher cycle.',
          '- Capacitor: see the formulas in `cases.json` sources. Stable percent is the stable equilibrium root; time to empty is the discrete simulation of the game (activations from t = 0, first unpayable activation ends the run; asserted to stay below the continuous closed form).',
          '- Hand check of the method (done on paper, not by the script): Rifter allIII, 200mm AutoCannon I cycle = 3750 ms x (1 - 7.5% x Minmatar Frigate) x (1 - 2% x Gunnery) x (1 - 4% x Rapid Firing) x Gyrostabilizer 0.92 (SDE attr 204) x Burst Aerator 0.9 (stack-penalised, second one); see the hand check below.',
          '  Rifter allIII (Minmatar Frigate III, Gunnery V, Rapid Firing III, Surgical Strike III, Small Projectile Turret V): skill factors 0.775 x 0.90 x 0.88 = 0.6138; penalised chain 0.90, 0.91309, 0.95434 (0.9 and 0.9 aerators, 0.92 gyrostabilizer) = 0.78425; cycle 3750 x 0.6138 x 0.78425 = 1805.1 ms. Damage multiplier 2.8875 x 1.25 (SPT V) x 1.09 (SS III) x 1.07 (gyro, alone) = 4.2095. DPS per gun 12 x 4.2095 / 1.8051 = 27.98; three guns 83.95, the figure of the Rifter allIII section.',
          '']
    for ref, pid in PAIRS:
        cases, doc = build_case(ref, pid)
        hull = parse_eft(FIT % ref)[0]
        out['pairs'].append({'id': '%d:%s' % (ref, pid), 'ref': ref, 'hull': hull, 'profile': pid, 'numbers': cases})
        md += ['## Reference %d %s x `%s`' % (ref, hull, pid), ''] + doc + ['']
    json.dump(out, open(os.path.join(HERE, 'cases.json'), 'w'), indent=1)
    open(os.path.join(HERE, 'worksheet.md'), 'w').write('\n'.join(md) + '\n')
