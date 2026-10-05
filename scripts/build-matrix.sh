#!/bin/sh
# Explicit acceptance matrix. Temporary configs/binaries stay outside the tree.
set -eu
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
go build -o "$scratch/qgramm-build" ./cmd/qgramm-build
python3 - "$scratch" <<'PY'
import pathlib, sys
root=pathlib.Path(sys.argv[1])
features='groups files e2ee calls delete edit reply forward reactions openai anthropic ai_streaming ai_policy ai_storage ai_endpoint mcp http_tools'.split()
profiles={'minimal':set(), 'full':set(features)}
for feature in features:
    profiles['without-'+feature]=set(features)-{feature}
    if feature=='ai_policy': profiles['without-'+feature].discard('ai_storage')
    if feature=='e2ee': profiles['without-'+feature].discard('ai_endpoint')
    minimum={feature}
    if feature=='ai_storage': minimum.update({'ai_policy','openai'})
    if feature=='ai_endpoint': minimum.add('e2ee')
    if feature in ('mcp','http_tools','ai_streaming','ai_policy'): minimum.add('openai')
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
    presets={
        'minimal':set(),
        'support':{'groups','files','delete','edit','reply','reactions'},
        'community':{'groups','files','delete','edit','reply','forward','reactions'},
        'ai-openai':{'openai'},
        'ai-anthropic':{'anthropic'},
    }
    for preset, enabled in presets.items():
        name='preset-'+preset
        (root/(name+'.toml')).write_text('preset="'+preset+'"\n'+base)
        manifest.write(name+'|'+','.join('qg_'+f for f in sorted(enabled))+'\n')
    for name, preset, overrides, enabled in [
        ('preset-support-override','support','files=false\n',presets['support']-{'files'}),
        ('preset-ai-disabled','ai-openai','openai=false\n',set()),
    ]:
        (root/(name+'.toml')).write_text('preset="'+preset+'"\n'+base+'[features]\n'+overrides)
        manifest.write(name+'|'+','.join('qg_'+f for f in sorted(enabled))+'\n')
    name='named-ai-network'
    (root/(name+'.toml')).write_text(pathlib.Path('configs/ai-network.toml').read_text())
    manifest.write(name+'|qg_ai_streaming,qg_groups,qg_openai\n')
    name='named-ai-policy'
    (root/(name+'.toml')).write_text(pathlib.Path('configs/ai-policy.toml').read_text())
    manifest.write(name+'|qg_ai_policy,qg_http_tools,qg_openai\n')
    for name, tags in [('ai-storage', 'qg_ai_storage,qg_ai_policy,qg_openai'), ('ai-endpoint-relay', 'qg_ai_endpoint,qg_e2ee')]:
        (root/(name+'.toml')).write_text(pathlib.Path('configs/'+name+'.toml').read_text())
        manifest.write(name+'|'+tags+'\n')
invalid=root/'invalid';invalid.mkdir()
cases={
    'storage-without-policy':base+'[features]\nai_storage=true\nopenai=true\n',
    'endpoint-without-e2ee':base+'[features]\nai_endpoint=true\n',
    'mcp-without-provider':base+'[features]\nmcp=true\n',
    'http-tools-without-provider':base+'[features]\nhttp_tools=true\n',
    'unknown-feature':base+'[features]\nunavailable=true\n',
    'plaintext-public':base.replace('127.0.0.1:8080','0.0.0.0:8080'),
    'literal-secret':base+'[security]\nmaster_key_env="not-an-env-reference"\n',
    'bad-policy':base+'[policy]\nhistory="arbitrary"\n',
    'tool-without-feature':base+'[features]\nopenai=true\n[[ai.tools]]\nname="bad"\nkind="http"\nurl="https://tools.example.com"\nmethods=["POST"]\n',
    'unknown-preset':'preset="nonexistent"\n'+base,
    'ai-preset-without-model':'preset="ai-openai"\n'+base.replace('model="test-model"\n',''),
    'stream-without-provider':base+'[features]\nai_streaming=true\n',
    'policy-without-provider':base+'[features]\nai_policy=true\n',
    'approval-without-policy':base+'[features]\nopenai=true\nhttp_tools=true\n[[ai.tools]]\nname="approved"\nkind="http"\nurl="https://tools.example.test"\nmethods=["POST"]\nrequire_approval=true\n',
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
names={'groups':'groups','files':'files','calls':'calls','delete':'delete','edit':'edit','reply':'reply','forward':'forward','reactions':'reactions','openai':'ai_openai','anthropic':'ai_anthropic','ai_streaming':'ai_stream_register','ai_policy':'ai_policy_runtime','mcp':'ai_mcp','http_tools':'ai_http_tools','e2ee':'e2ee','ai_storage':'ai_storage','ai_endpoint':'ai_endpoint'}
for feature,filename in names.items():
    assert ((filename+'.go') in files)==(('qg_'+feature) in tags), (profile,feature,'compiled file selection mismatch')
deps=(root/(profile+'-deps')).read_text()
assert ('github.com/mgg789/QGramm/internal/aivault' in deps)==('qg_ai_storage' in tags), (profile,'storage dependency selection mismatch')
assert 'github.com/mgg789/QGramm/internal/microsafer' not in deps, (profile,'endpoint process included in core')
assert 'github.com/redis/go-redis/v9' not in deps, (profile,'removed Redis dependency present')
assert ('github.com/thomas-vilte/mls-go' in deps)==('qg_e2ee' in tags), (profile,'MLS dependency selection mismatch')
PY
done < "$scratch/profiles"
QGRAMM_MATRIX_INVALID_DIR="$scratch/invalid" go test ./cmd/qgramm-build -run '^TestMatrixInvalidConfigurations$' -count=1
go test ./internal/config ./cmd/qgramm-build
go test -tags qg_groups,qg_files,qg_e2ee,qg_calls,qg_delete,qg_edit,qg_reply,qg_forward,qg_reactions,qg_openai,qg_anthropic,qg_ai_streaming,qg_ai_policy,qg_ai_storage,qg_ai_endpoint,qg_mcp,qg_http_tools ./...
python3 - "$scratch" <<'PY'
import itertools,pathlib,sys
root=pathlib.Path(sys.argv[1])
with (root/'micro-profiles').open('w') as out:
    for enabled in itertools.product((False,True), repeat=4):
        selected=[f'qg_{name}' for name,on in zip(('openai','http_tools','mcp','ai_storage'),enabled) if on]
        out.write(','.join(['qg_ai_endpoint','qg_e2ee']+selected)+'\n')
PY
while IFS= read -r tags; do
  go build -tags "$tags" -o "$scratch/qgramm-micro-safer" ./cmd/qgramm-micro-safer
  go list -tags "$tags" -f '{{range .GoFiles}}{{println .}}{{end}}' ./internal/microsafer > "$scratch/micro-files"
  go list -tags "$tags" -deps ./cmd/qgramm-micro-safer > "$scratch/micro-deps"
  TAGS="$tags" SCRATCH="$scratch" python3 - <<'PY'
import os,pathlib
root=pathlib.Path(os.environ['SCRATCH']); tags=set(os.environ['TAGS'].split(','))
files=set((root/'micro-files').read_text().splitlines())
for tag,implementations in {
    'qg_openai':['model.go','model_adapter_openai.go'],
    'qg_http_tools':['http_adapter_open.go'],
    'qg_mcp':['tools.go','mcp_adapter_open.go'],
    'qg_ai_storage':['storage_qg.go'],
}.items():
    for implementation in implementations:
        assert (implementation in files)==(tag in tags), (tags,implementation,'micro implementation selection mismatch')
deps=(root/'micro-deps').read_text()
assert ('github.com/mgg789/QGramm/internal/aivault' in deps)==('qg_ai_storage' in tags), (tags,'micro vault dependency selection mismatch')
assert 'github.com/mgg789/QGramm/internal/core' not in deps, 'core included in external endpoint'
PY
done < "$scratch/micro-profiles"
"$scratch/qgramm-build" build -target micro-safer -config configs/ai-endpoint-relay.toml -out "$scratch/micro-base"
"$scratch/qgramm-build" build -target micro-safer -config configs/full.toml -out "$scratch/micro-full"
echo '47 core and 16 micro-safer build selections, 14 invalid configurations passed; no calibrated capacity claim'
