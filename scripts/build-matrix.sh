#!/bin/sh
# Explicit acceptance matrix. Temporary configs/binaries stay outside the tree.
set -eu
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
go build -o "$scratch/qgramm-build" ./cmd/qgramm-build
python3 - "$scratch" <<'PY'
import pathlib, sys
root=pathlib.Path(sys.argv[1])
features='groups files e2ee calls delete edit reply forward reactions openai anthropic mcp http_tools'.split()
profiles={'minimal':set(), 'full':set(features)}
for feature in features:
    profiles['without-'+feature]=set(features)-{feature}
    minimum={feature}
    if feature in ('mcp','http_tools'): minimum.add('openai')
    profiles['only-'+feature]=minimum
base='''[server]
listen="127.0.0.1:8080"
allow_insecure_loopback=true
[ai]
model="test-model"
openai_key_env="QGRAMM_OPENAI_KEY"
anthropic_key_env="QGRAMM_ANTHROPIC_KEY"
[calls]
turn_urls=["turns:turn.example.com:5349"]
turn_secret_env="QGRAMM_TURN_SECRET"
'''
with (root/'profiles').open('w') as manifest:
    for name, enabled in profiles.items():
        config=base+'[features]\n'+''.join(f'{f}={str(f in enabled).lower()}\n' for f in features)
        (root/(name+'.toml')).write_text(config)
        tags=','.join('qg_'+f for f in sorted(enabled))
        manifest.write(name+'|'+tags+'\n')
invalid=root/'invalid';invalid.mkdir()
cases={
    'mcp-without-provider':base+'[features]\nmcp=true\n',
    'http-tools-without-provider':base+'[features]\nhttp_tools=true\n',
    'unknown-feature':base+'[features]\nunavailable=true\n',
    'plaintext-public':base.replace('127.0.0.1:8080','0.0.0.0:8080'),
    'literal-secret':base+'[security]\nmaster_key_env="not-an-env-reference"\n',
    'bad-policy':base+'[policy]\nhistory="arbitrary"\n',
    'tool-without-feature':base+'[features]\nopenai=true\n[[ai.tools]]\nname="bad"\nkind="http"\nurl="https://tools.example.com"\nmethods=["POST"]\n',
}
for name, config in cases.items(): (invalid/(name+'.toml')).write_text(config)
PY
while IFS='|' read -r profile tags; do
  echo "matrix: $profile"
  "$scratch/qgramm-build" build -config "$scratch/$profile.toml" -out "$scratch/qgramm-$profile"
  QGRAMM_MATRIX_CONFIG="$scratch/$profile.toml" go test -tags "$tags" ./cmd/qgramm-build -run '^TestMatrixRuntimeSelection$' -count=1
  go list -tags "$tags" -deps ./cmd/qgramm > "$scratch/$profile-deps"
  go list -tags "$tags" -f '{{range .GoFiles}}{{println .}}{{end}}' ./internal/modules > "$scratch/$profile-files"
  PROFILE="$profile" TAGS="$tags" SCRATCH="$scratch" python3 - <<'PY'
import os,pathlib
root=pathlib.Path(os.environ['SCRATCH']);profile=os.environ['PROFILE'];tags=set(os.environ['TAGS'].split(','))
files=set((root/(profile+'-files')).read_text().splitlines())
names={'groups':'groups','files':'files','calls':'calls','delete':'delete','edit':'edit','reply':'reply','forward':'forward','reactions':'reactions','openai':'ai_openai','anthropic':'ai_anthropic','mcp':'ai_mcp','http_tools':'ai_http_tools','e2ee':'e2ee'}
for feature,filename in names.items():
    assert ((filename+'.go') in files)==(('qg_'+feature) in tags), (profile,feature,'compiled file selection mismatch')
deps=(root/(profile+'-deps')).read_text()
assert 'github.com/redis/go-redis/v9' not in deps, (profile,'removed Redis dependency present')
assert ('github.com/thomas-vilte/mls-go' in deps)==('qg_e2ee' in tags), (profile,'MLS dependency selection mismatch')
PY
done < "$scratch/profiles"
QGRAMM_MATRIX_INVALID_DIR="$scratch/invalid" go test ./cmd/qgramm-build -run '^TestMatrixInvalidConfigurations$' -count=1
go test ./internal/config ./cmd/qgramm-build
go test -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_mcp,qg_http_tools ./...
echo '28 build/runtime selections and 7 invalid configurations passed; no calibrated capacity claim'
