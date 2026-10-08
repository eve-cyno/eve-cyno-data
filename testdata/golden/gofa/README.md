# Gofa Dogma Engine — Validation Oracle

This directory holds the validation oracle for the Gofa Go dogma engine
(`fit/gofa`): attribute-level convergence targets. The engine's output is
compared against these numbers in the tests; they are an external reference
for checking the engine's results, not part of the engine.

**What these files are (and are not).** Each `oracle/*.json` is a numeric dump:
CCP attribute data plus numbers computed by running a separately obtained,
locally cloned [Pyfa](https://github.com/pyfa-org/Pyfa) (GPL-3.0) as an
external validation reference. No Pyfa source code, data file or binary is
part of this repository, and Gofa does not import or link Pyfa; the numbers
are only compared against Gofa's output in tests. Pyfa and its authors are
credited here as the source of the reference values.

## Provenance

| Item | Value |
|---|---|
| Pyfa commit | `17721f16e5a6327c02c4a2e089d1f217d6079f09` (v2.67.0) |
| Python | CPython 3.12 |
| sqlalchemy | 1.4.50 |
| logbook | 1.7.0.post0 |
| pyyaml | 6.0.1 |
| python-dateutil | 2.8.2 |
| Skills | All V (Character.getAll5()) |
| Damage pattern | Uniform 25/25/25/25 (fit.damagePattern = None) |
| Factor reload | False |

## Corpus (`corpus.json`)

A JSON list of fit entries. Each entry has:

```json
{
  "name": "fit-slug",
  "eft":  "<EFT multiline string>",
  "alpha": false
}
```

| Name | Ship | Purpose |
|---|---|---|
| `rifter-turret` | Rifter | Turret frigate; the spike baseline |
| `caracal-missile` | Caracal | Shield cruiser + missiles |
| `maller-armor` | Maller | Armor cruiser + active rep |
| `vexor-drone` | Vexor | Drone boat |
| `firetail-mwd` | Republic Fleet Firetail | MWD frigate; nav/sig test |
| `maller-cap-heavy` | Maller | Large active armor; cap stability test |

## Oracle file schema (`oracle/<name>.json`)

Each file is a JSON object with three top-level keys:

### `_meta`

Provenance metadata; not used by the test harness for value comparisons.

```
_meta.fit_label          string  — matches the corpus "name"
_meta.ship               string  — ship name as passed to eos.db.getItem
_meta.skills             string  — "All V"
_meta.pyfa_commit        string  — pinned Pyfa commit SHA
_meta.eos_gamedata_build string  — Pyfa/eos gamedata build number
_meta.factor_reload      bool    — false (do not factor reload time into DPS)
_meta.damage_pattern     string  — "uniform"
```

### `attrs`

Per-item complete modified attribute dump. Keys are role-based labels:

```
"ship"                        — the hull
"module:<name>#<n>"           — the n-th instance of module <name> (1-indexed)
"charge:<name>#<n>"           — charge loaded in module instance n
"drone:<name>#<n>"            — the n-th instance of drone <name> (1-indexed)
```

Each entry has:

```
typeID              int     — EVE typeID
name                string  — typeName from eve.db
role                string  — "ship" | "module" | "charge" | "drone"
slot_index          int     — 0-based slot index (modules and drones only)
state               string  — eos FittingModuleState value (modules only)
loaded_in           string  — parent module label (charges only)
modifiedAttributes  object  — { "<attributeName>": <float|null>, ... }
```

`modifiedAttributes` is the COMPLETE set of attributes for the item template
(all keys from `itemModifiedAttributes.original`) with values as computed by
eos after a full `calculateModifiedAttributes()` pass. This is the
attribute-level convergence target.

Key attribute names for convergence testing:

| Attribute name | Attribute ID | Meaning |
|---|---|---|
| `speed` | 51 | Weapon cycle time (ms); also known as RoF |
| `damageMultiplier` | 64 | Turret/launcher damage multiplier |
| `maxRange` | 54 | Optimal range (m) |
| `falloff` | 158 | Falloff (m) |
| `trackingSpeed` | 160 | Turret tracking speed |
| `armorHP` | 265 | Armor HP |
| `shieldCapacity` | 263 | Shield HP |
| `hp` | 9 | Hull HP (structural) |
| `capacitorCapacity` | 482 | Capacitor capacity (GJ) |
| `maxVelocity` | 37 | Maximum velocity (m/s) |
| `agility` | 70 | Inertia modifier |
| `signatureRadius` | 552 | Signature radius (m) |
| `scanResolution` | 564 | Scan resolution (mm) |
| `maxTargetRange` | 76 | Maximum targeting range (m) |

### `stats`

Top-line fit statistics. All values are floats except booleans.

```
stats.dps.total             float   — combined weapon + drone DPS
stats.dps.weapon            float   — weapon DPS only
stats.dps.drone             float   — drone DPS only
stats.volley.total          float   — single volley damage

stats.ehp.shield            float   — effective shield HP (uniform damage pattern)
stats.ehp.armor             float   — effective armor HP
stats.ehp.hull              float   — effective hull HP
stats.ehp.total             float   — sum of shield+armor+hull EHP

stats.hp.shield             float   — raw shield HP
stats.hp.armor              float   — raw armor HP
stats.hp.hull               float   — raw hull HP

stats.cap.capacity          float   — capacitor capacity (GJ)
stats.cap.stable            bool    — true if cap is stable
stats.cap.state             float   — % remaining cap if stable; seconds to empty if not

stats.nav.maxVelocity       float   — maximum velocity (m/s)
stats.nav.alignTime         float   — align time (seconds)
stats.nav.signatureRadius   float   — signature radius (m)
stats.nav.warpSpeed         float   — warp speed (AU/s)
stats.nav.mass              float   — total mass (kg)

stats.targeting.scanResolution    float — scan resolution (mm)
stats.targeting.maxTargetRange    float — max targeting range (m)
stats.targeting.maxLockedTargets  float — max locked targets

stats.drones.bandwidth      float   — drone bandwidth (Mbit/s)
stats.drones.capacity       float   — drone bay capacity (m3)
```

## Regenerating the oracle

The oracle files were produced by a small driver script that loads each fit of
`corpus.json` into a local Pyfa checkout (pinned to the commit in the table
above, with its own Python environment and game-data database) and dumps the
attributes and statistics described above. That script and the Pyfa checkout
live outside this repository and are not distributed with it; the committed
JSON files are the complete, reviewable result. Changing them means
re-running the reference tool at the pinned commit and replacing the files.
