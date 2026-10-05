#!/usr/bin/env python3
"""Maintain explicit wire contracts and fail when Go route coverage changes.

Run from the repository root: python3 scripts/generate-contract.py
Schemas are reviewed static maps of the actual Go JSON contracts, not an AST
inference engine. The exact route set is asserted against source on every run.
"""
import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parents[1]
S = {"type": "string"}
I = {"type": "integer", "format": "int64"}
N = {**I, "minimum": 0}
B = {"type": "boolean"}
BASE64 = {**S, "contentEncoding": "base64"}
ID = {**S, "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9_.-]+$"}
HASH = {**S, "pattern": "^[0-9a-f]{64}$"}

def enum(*values): return {**S, "enum": list(values)}
def arr(items): return {"type": "array", "items": items}
def ref(name): return {"$ref": "#/components/schemas/" + name}
def obj(properties, required=None):
    return {"type": "object", "properties": properties, "required": list(properties) if required is None else required, "additionalProperties": False}

C = {}
C["Error"] = obj({"error": S})
C["Envelope"] = obj({"key_id": S, "enc": BASE64, "ciphertext": BASE64})
C["MessageInput"] = obj({"operation_id": ID, "envelope": {"anyOf": [ref("Envelope"), {"type": "null"}]}, "mls": BASE64, "epoch": N, "attachments": arr(S), "reply_to": S, "forward_from": S}, ["operation_id"])
C["MessageInput"]["description"] = "basic requires envelope; e2ee requires MLS PrivateMessage and current epoch. Attachment/reply/forward fields require compiled features. No plaintext JSON payload field."
C["Message"] = obj({"id": S, "chat_id": S, "sender": S, "device_id": S, "operation_id": S, "seq": N, "revision": I, "deleted": B, "mode": enum("basic", "e2ee"), "epoch": N, "created_at": I, "envelope": ref("Envelope"), "mls": BASE64, "attachments": arr(S), "reply_to": S, "forward_from": S}, ["id", "chat_id", "sender", "device_id", "operation_id", "seq", "revision", "deleted", "mode", "created_at"])
C["Reaction"] = obj({"type": N, "user_id": S})
C["Message"]["properties"]["reactions"] = arr(ref("Reaction"))
C["AcceptanceReceipt"] = obj({"message_id": S, "chat_id": S, "operation_id": S, "seq": N})
C["SendResult"] = {"oneOf": [obj({"status": {"const": "accepted"}, "message": ref("Message")}), obj({"status": {"const": "accepted"}, "receipt": ref("AcceptanceReceipt")})]}
C["Chat"] = obj({"id": S, "kind": enum("direct", "group"), "mode": enum("basic", "e2ee"), "seq": N, "epoch": N, "pending_rekey": B, "role": enum("owner", "admin", "member"), "can_send": B})
C["RosterMember"] = obj({"device_id": S, "leaf_index": {"type": "integer", "minimum": 0, "maximum": 65535}})
C["ChunkBinding"] = obj({"chat_id": S, "user_id": S, "device_id": S, "operation_id": S})
C["MembershipEvent"] = obj({"user_id": S, "active": B, "can_send": B})
C["ReceiptEvent"] = obj({"user_id": S, "device_id": S, "delivered": N, "read": N})
C["ReactionEvent"] = obj({"message_id": S, "user_id": S, "type": N, "remove": B})
C["MLSCommitEvent"] = obj({"transition_id": S, "epoch": N, "commit_hash": HASH})
C["MLSEpochEvent"] = obj({"transition_id": S, "epoch": N, "roster": arr(ref("RosterMember"))})
C["AIIdentityEvent"] = obj({"user_id": S, "device_id": S, "provider": enum("openai", "anthropic")})
C["CallRingingEvent"] = obj({"id": S, "caller": S, "mode": enum("audio", "video")})
C["CallSignalEvent"] = obj({"id": S, "sender": S, "device_id": S, "operation_id": S, "envelope": ref("Envelope"), "to_device": S, "to_user": S, "mls": BASE64, "epoch": N}, ["id", "sender", "device_id", "operation_id"])
C["CallSignalEvent"]["properties"]["type"] = enum("accept", "reject", "end", "offer", "answer", "ice")
C["CallSignalEvent"]["required"].append("type")
C["CallTimeoutEvent"] = obj({"id": S, "reason": enum("timeout")})
event_data = {
    "message.created": "Message", "message.edited": "Message", "message.deleted": "Message", "message.hidden": "Message",
    "membership.changed": "MembershipEvent", "receipt.updated": "ReceiptEvent", "reaction.updated": "Message",
    "mls.commit.pending": "MLSCommitEvent", "mls.epoch.confirmed": "MLSEpochEvent", "ai.participant.created": "AIIdentityEvent",
    "call.ringing": "CallRingingEvent", **{"call." + x: "CallSignalEvent" for x in ("accept", "reject", "offer", "answer", "ice")},
}
events = []
for kind, data in event_data.items():
    required = ["type", "seq", "chat_id", "data"] + (["message_id"] if data == "Message" else [])
    events.append(obj({"type": {"const": kind}, "seq": N, "chat_id": S, "message_id": S, "data": ref(data)}, required))
events.append(obj({"type": {"const": "call.end"}, "seq": N, "chat_id": S, "data": {"oneOf": [ref("CallSignalEvent"), ref("CallTimeoutEvent")]}}, ["type", "seq", "chat_id", "data"]))
C["Event"] = {"oneOf": events}
C["MutationInput"] = obj({"operation_id": S, "expected_revision": I, "message": ref("MessageInput"), "type": I, "remove": B}, ["operation_id"])
C["MessageReplacement"] = obj(C["MessageInput"]["properties"], [])
C["MutationInput"]["properties"]["message"] = ref("MessageReplacement")
C["MutationInput"]["description"] = "PATCH requires expected_revision matching current revision and message replacement; the nested operation_id is overridden by the outer operation_id. DELETE and reaction mutations use the same decoded type but ignore unrelated fields."
C["MutationResult"] = {"oneOf": [obj({"updated": B}), obj({"id": S})]}
C["UploadCreate"] = obj({"operation_id": {**S, "minLength": 1, "maxLength": 128}, "size": {**I, "minimum": 1}, "chunks": {**I, "minimum": 1, "maximum": 65536}, "sha256": HASH, "envelope": {"anyOf": [ref("Envelope"), {"type": "null"}]}}, ["operation_id", "size", "chunks", "sha256"])
C["UploadCreate"]["description"] = "basic envelope wraps 32-byte file key; e2ee envelope must be absent. Sizes and hashes describe encrypted wire chunks; configured size/quota/chunk bounds apply."
C["UploadCreated"] = obj({"id": S, "state": enum("uploading", "ready", "deleted"), "expires_at": I})
C["UploadStatus"] = obj({"id": S, "state": enum("uploading", "ready"), "mode": enum("basic", "e2ee"), "size": I, "chunks": arr(obj({"index": N, "size": I, "sha256": HASH})), "total_chunks": I, "sha256": HASH, "expires_at": I, "chunk_binding": ref("ChunkBinding")})
C["MLSConfirm"] = obj({"transition_id": S, "group_id": BASE64, "group_context": BASE64, "previous_epoch": {**I, "minimum": -1}, "epoch": N, "signer_device": S, "signature": BASE64, "roster": arr(ref("RosterMember"))}, ["group_id", "group_context", "previous_epoch", "epoch", "signer_device", "signature", "roster"])
C["MLSConfirm"]["description"] = "previous_epoch=-1 initializes a new relay. Otherwise transition_id is required. Signature is Ed25519 over canonical MLSRosterProof JSON, not over this request body. See RFC relay documentation."
C["MLSRosterProof"] = obj({"version": {"const": 1}, "chat_id": S, "group_id": BASE64, "previous_epoch": I, "epoch": N, "commit_hash": S, "context_hash": HASH, "roster": arr(ref("RosterMember"))})
C["MLSRosterProof"]["description"] = "Exact declaration field order shown here; sort roster by device_id, use Go encoding/json escaping. commit_hash is empty for initialization, otherwise SHA256 wire commit. context_hash=SHA256 binary GroupContext."
C["CallSignalInput"] = obj({"type": enum("accept", "reject", "end", "offer", "answer", "ice"), "operation_id": S, "to_device": S, "envelope": {"anyOf": [ref("Envelope"), {"type": "null"}]}, "mls": BASE64, "epoch": N}, ["type", "operation_id"])
C["CallSignalPlaintext"] = obj({"sdp": S, "candidate": S, "sdp_mid": S, "sdp_mline_index": {"anyOf": [N, {"type": "null"}]}}, [])
C["AIJob"] = obj({"id": S, "message_id": S, "status": enum("queued", "running", "succeeded", "failed", "uncertain", "cancelled"), "result_id": S, "created_at": I, "updated_at": I})
C["AITask"] = obj({**C["AIJob"]["properties"], "chat_id": S, "agent_id": S})
C["AIProgressChunk"] = obj({"chunk": N, "kind": enum("text.delta", "tool.started", "tool.arguments.delta"), "operation_id": S, "sender": S, "device_id": S, "mode": enum("basic", "e2ee"), "epoch": N, "envelope": ref("Envelope"), "mls": BASE64}, ["chunk", "kind", "operation_id", "sender", "device_id", "mode", "epoch"])
C["AIProgressChunk"]["description"] = "BASIC envelope or MLS ciphertext; Binding(chat,sender,device,operation_id). operation_id=ai-progress-{job}-{chunk}. Parts are previews; final message/job status establishes completion."
C["Event"]["oneOf"] += [obj({"type": {"const": kind}, "seq": N, "chat_id": S, "data": data}) for kind, data in {
    "ai.agent.attached": obj({"agent_id": S}),
    "ai.tasks.queued": obj({"source_message_id": S, "agents": arr(S)}),
    "ai.progress.available": obj({"job_id": S, "chunk": N, "kind": enum("text.delta", "tool.started", "tool.arguments.delta")}),
    "ai.job.cancelled": obj({"job_id": S}),
}.items()]

# route -> (request schema or None, success response schema, success codes)
ROUTES = {
"GET /healthz": (None, obj({"status": {"const": "ok"}}), [200]),
"GET /readyz": (None, obj({"status": {"const": "ready"}}), [200]),
"GET /v1/capabilities": (None, obj({"protocol_version": {"const": 1}, "features": arr(S), "server_key": BASE64, "server_key_id": HASH, "hpke_suite": {"const": "X25519-HKDF-SHA256-AES128GCM"}, "delivery": {"const": "at-least-once"}, "event_retention_hours": I, "dedup_retention_hours": I, "history": enum("all", "since_join"), "delete_mode": enum("global", "author_only"), "reaction_types": arr(S), "max_batch": I, "ai_trust_boundary": S}, required=["protocol_version", "features", "server_key", "server_key_id", "hpke_suite", "delivery", "event_retention_hours", "dedup_retention_hours", "history", "delete_mode", "reaction_types", "max_batch", "ai_trust_boundary"]), [200]),
"POST /v1/ws-tickets": (None, obj({"ticket": S, "expires_in": {"const": 30}}), [201]),
"GET /v1/ws": (None, None, [101]),
"GET /v1/chats": (None, arr(ref("Chat")), [200]),
"GET /v1/chats/{chat}/messages": (None, arr(ref("Message")), [200]),
"POST /v1/chats/{chat}/messages": (ref("MessageInput"), ref("SendResult"), [201]),
"POST /v1/chats/{chat}/messages/batch": (obj({"messages": {**arr(ref("MessageInput")), "minItems": 1}}), arr({"oneOf": [obj({"operation_id": S, "status": {"const": 201}, "message": ref("Message")}), obj({"operation_id": S, "status": {"const": 201}, "receipt": ref("AcceptanceReceipt")}), obj({"operation_id": S, "status": I, "error": S})]}), [207]),
"GET /v1/chats/{chat}/events": (None, arr(ref("Event")), [200]),
"POST /v1/chats/{chat}/receipts": (obj({"delivered": N, "read": N}, []), obj({"acknowledged": B}), [200]),
"PUT /management/v1/users/{user}": (obj({"disabled": B}, []), obj({"id": S, "disabled": B}), [200]),
"PUT /management/v1/users/{user}/devices/{device}": (obj({"public_key": BASE64, "signing_key": BASE64}, ["public_key"]), obj({"id": S, "user_id": S}), [200]),
"DELETE /management/v1/users/{user}/devices/{device}": (None, obj({"revoked": B}), [200]),
"POST /management/v1/chats/direct": (obj({"id": ID, "mode": enum("basic", "e2ee"), "members": {**arr(ID), "minItems": 2, "maxItems": 2, "uniqueItems": True}}, ["members"]), obj({"id": S, "kind": {"const": "direct"}, "mode": enum("basic", "e2ee"), "pending_rekey": B}), [201]),
"POST /management/v1/chats/groups": (obj({"id": ID, "mode": enum("basic", "e2ee"), "members": {**arr(ID), "minItems": 2, "uniqueItems": True}}, ["members"]), obj({"id": S, "kind": {"const": "group"}, "mode": enum("basic", "e2ee"), "pending_rekey": B}), [201]),
"PUT /management/v1/chats/{chat}/members/{user}": (obj({"role": enum("owner", "admin", "member"), "can_send": B, "active": B, "allow_history": B}, ["role"]), obj({"updated": B}), [200]),
"PATCH /v1/chats/{chat}/messages/{message}": (ref("MutationInput"), ref("MutationResult"), [200]),
"DELETE /v1/chats/{chat}/messages/{message}": (ref("MutationInput"), ref("MutationResult"), [200]),
"GET /v1/chats/{chat}/messages/{message}/reactions": (None, arr(obj({"type": I, "user_id": S})), [200]),
"POST /v1/chats/{chat}/messages/{message}/reactions": (ref("MutationInput"), ref("MutationResult"), [200]),
"POST /v1/chats/{chat}/uploads": (ref("UploadCreate"), ref("UploadCreated"), [200, 201]),
"GET /v1/uploads/{upload}": (None, ref("UploadStatus"), [200]),
"PUT /v1/uploads/{upload}/chunks/{index}": ({"type": "string", "format": "binary"}, obj({"index": N}), [200, 201]),
"POST /v1/uploads/{upload}/complete": (None, obj({"id": S, "state": enum("ready")}), [200]),
"GET /v1/uploads/{upload}/chunks/{index}": (None, {"type": "string", "format": "binary"}, [200]),
"GET /v1/uploads/{upload}/key": (None, obj({"envelope": ref("Envelope"), "upload_id": S, "chunk_binding": ref("ChunkBinding")}), [200]),
"GET /v1/calls/turn": (None, obj({"urls": arr(S), "username": S, "credential": S, "expires_at": I}), [200]),
"POST /v1/chats/{chat}/calls": (obj({"mode": enum("audio", "video")}), obj({"id": S, "state": {"const": "ringing"}, "seq": N}), [201]),
"POST /v1/calls/{call}/signals": (ref("CallSignalInput"), obj({"seq": N, "state": enum("ringing", "active", "ended")}), [200]),
"POST /v1/mls/keypackages": (obj({"key_package": BASE64}), obj({"id": S}), [201]),
"POST /v1/chats/{chat}/mls/keypackages/{device}/claim": (None, obj({"id": S, "device_id": S, "key_package": BASE64}), [200]),
"POST /v1/chats/{chat}/mls/commits": (obj({"commit": BASE64, "welcomes": arr(obj({"device_id": S, "key_package_id": S, "welcome": BASE64}))}, ["commit"]), obj({"transition_id": S, "commit_hash": HASH, "pending_rekey": {"const": True}}), [202]),
"GET /v1/chats/{chat}/mls": (None, obj({"group_id": BASE64, "group_context": BASE64, "epoch": N, "pending_rekey": B, "roster": arr(ref("RosterMember")), "suite": {"const": 1}}), [200]),
"GET /v1/chats/{chat}/mls/inbox": (None, obj({"welcomes": arr(obj({"id": S, "epoch": N, "kind": enum("welcome"), "wire": BASE64})), "commits": arr(obj({"id": S, "epoch": N, "commit_hash": HASH, "wire": BASE64, "confirmed": B})), "next_welcome": S, "next_commit": S}), [200]),
"POST /management/v1/chats/{chat}/mls/confirm": (ref("MLSConfirm"), obj({"epoch": N, "pending_rekey": {"const": False}}), [200]),
"POST /management/v1/ai/participants": (obj({"user_id": S, "device_id": S, "provider": enum("openai", "anthropic"), "mode": enum("basic", "e2ee"), "key_package": BASE64, "tools": arr(S)}, ["user_id", "device_id", "provider"]), obj({"chat_id": S, "user_id": S, "device_id": S, "provider": enum("openai", "anthropic"), "mode": enum("basic", "e2ee"), "epoch": N, "welcome": BASE64, "tools": {"anyOf": [arr(S), {"type": "null"}]}}), [201]),
"PUT /management/v1/ai/chats/{chat}/tools": (obj({"tools": arr(S)}, []), obj({"tools": {"anyOf": [arr(S), {"type": "null"}]}}), [200]),
"GET /management/v1/ai/chats/{chat}/jobs": (None, obj({"jobs": arr(ref("AIJob"))}), [200]),
"POST /management/v1/ai/agents": (obj({"bot_name": ID, "user_id": ID, "tools": arr(S)}, ["bot_name"]), obj({"agent_id": S, "bot_name": S, "user_id": S, "device_id": S, "provider": enum("openai", "anthropic"), "version": I, "tools": {"anyOf": [arr(S), {"type": "null"}]}}), [201]),
"POST /management/v1/ai/agents/{agent}/chats/{chat}": (obj({"tools": arr(S)}, []), obj({"chat_id": S, "agent_id": S, "user_id": S, "device_id": S, "version": I, "tools": {"anyOf": [arr(S), {"type": "null"}]}}), [201]),
"POST /v1/chats/{chat}/ai/tasks": (obj({"message_id": ID, "agents": {**arr(ID), "minItems": 1, "maxItems": 16, "uniqueItems": True}}), obj({"chat_id": S, "message_id": S, "task_ids": arr(S)}), [202]),
"GET /v1/chats/{chat}/ai/tasks": (None, obj({"tasks": arr(ref("AITask"))}), [200]),
"GET /v1/chats/{chat}/ai/tasks/{task}": (None, ref("AITask"), [200]),
"DELETE /v1/chats/{chat}/ai/tasks/{task}": (None, obj({"task_id": S, "status": C["AIJob"]["properties"]["status"]}), [200]),
"GET /v1/chats/{chat}/ai/jobs/{job}/progress": (None, obj({"job_id": S, "status": C["AIJob"]["properties"]["status"], "chunks": arr(ref("AIProgressChunk"))}), [200]),
"POST /v1/chats/{chat}/ai/jobs/{job}/cancel": (None, obj({"job_id": S, "status": C["AIJob"]["properties"]["status"]}), [200]),
}

def parameter(name, where, schema, required=False):
    return {"name": name, "in": where, "required": required, "schema": schema}

def main():
    sources = list((ROOT / "internal/core").glob("*.go")) + list((ROOT / "internal/modules").glob("*.go"))
    actual = set()
    locations = {}
    for source in sources:
        if source.name.endswith("_test.go"): continue
        for route in re.findall(r'(?:AddRoute|AddManagementRoute|HandleFunc)\("((?:GET|POST|PUT|PATCH|DELETE) /[^" ]+)"', source.read_text()):
            actual.add(route); locations[route] = str(source.relative_to(ROOT))
    missing, stale = actual - ROUTES.keys(), ROUTES.keys() - actual
    assert not missing and not stale, f"Route coverage drift: undocumented={sorted(missing)}, stale={sorted(stale)}"
    paths = {}
    for route, (request, response, codes) in ROUTES.items():
        method, path = route.split(" ", 1)
        params = [parameter(name, "path", N if name == "index" else S, True) for name in re.findall(r"{([^}]+)}", path)]
        if path.endswith("/messages") and method == "GET": params += [parameter("after", "query", N), parameter("limit", "query", {**N, "minimum": 1, "maximum": 200})]
        if path.endswith("/events"): params += [parameter("after", "query", N, True)]
        if path.endswith("/progress"): params += [parameter("after", "query", N)]
        if path.endswith("/mls/inbox"): params += [parameter("after_welcome", "query", S), parameter("after_commit", "query", S)]
        if path == "/v1/ws": params += [parameter("ticket", "query", S, True), parameter("Origin", "header", S)]
        binary = "chunks/{index}" in path
        if binary and method == "PUT": params += [parameter("X-Chunk-SHA256", "header", HASH, True)]
        op = {"operationId": re.sub(r"[^a-zA-Z0-9]+", "_", route).strip("_"), "summary": route, "x-source": locations[route], "parameters": params, "responses": {"default": {"description": "JSON error; validation, authorization, conflict, quota/capacity or storage failure", "content": {"application/json": {"schema": ref("Error")}}}}}
        minimal_send = method == "POST" and path in ("/v1/chats/{chat}/messages", "/v1/chats/{chat}/messages/batch")
        if minimal_send:
            params.append(parameter("Prefer", "header", {**S, "example": "return=minimal"}))
            op["description"] = "Default returns the current full message. Prefer: return=minimal returns a stable acceptance receipt without re-encrypting content for the sender; delivery remains a separate device receipt. Exact retries retain the operation result and check current access."
        op["security"] = [] if path in ("/healthz", "/readyz", "/v1/ws") else [{"managementBearer" if path.startswith("/management") else "deviceJWT": []}]
        if request:
            op["requestBody"] = {"required": True, "content": {"application/octet-stream" if binary else "application/json": {"schema": request}}}
        for code in codes:
            result = {"description": "WebSocket upgrade; frames in websocket.schema.json" if code == 101 else "Successful response"}
            if minimal_send:
                result["headers"] = {"Preference-Applied": {"description": "Present when the compact receipt was requested.", "schema": {"const": "return=minimal"}}}
            if response: result["content"] = {"application/octet-stream" if binary and method == "GET" else "application/json": {"schema": response}}
            op["responses"][str(code)] = result
        paths.setdefault(path, {})[method.lower()] = op
    spec = {"openapi": "3.1.0", "info": {"title": "QGramm", "version": "1", "description": "All compiled-feature routes. A binary exposes only selected modules; query capabilities. Schemas are maintained maps checked for exact source route coverage. Deployment policy sets runtime size/retention limits."}, "paths": paths, "components": {"schemas": C, "securitySchemes": {"deviceJWT": {"type": "http", "scheme": "bearer", "bearerFormat": "EdDSA JWT", "description": "External authority signed JWT with sub=user_id, device_id, iss, aud, iat and exp; lifetime at most15 minutes."}, "managementBearer": {"type": "http", "scheme": "bearer", "description": "Runtime management secret, separate from device JWT."}}}}
    def ws_refs(value):
        if isinstance(value, dict): return {k: (v.replace("#/components/schemas/", "#/$defs/") if k == "$ref" else ws_refs(v)) for k, v in value.items()}
        if isinstance(value, list): return [ws_refs(v) for v in value]
        return value
    definitions = ws_refs(C)
    definitions["Subscribe"] = obj({"type": {"const": "subscribe"}, "chat_id": S, "after": N}, ["type", "chat_id"])
    definitions["Unsubscribe"] = obj({"type": {"const": "unsubscribe"}, "chat_id": S, "after": I}, ["type", "chat_id"])
    # Reader uses json.Unmarshal and accepts extra command fields; these schemas
    # describe the intended interoperable shape rather than stricter parsing.
    definitions["Subscribe"]["additionalProperties"] = True
    definitions["Unsubscribe"]["additionalProperties"] = True
    definitions["SubscriptionError"] = obj({"type": {"const": "error"}, "error": S})
    definitions["SyncError"] = obj({"type": {"const": "sync.error"}, "chat_id": S, "error": S})
    ws = {"$schema": "https://json-schema.org/draft/2020-12/schema", "title": "QGramm WebSocket JSON frames", "description": "Client sends subscribe/unsubscribe. Server sends durable Event or error frames. No WS ack message: use POST /v1/chats/{chat}/receipts HTTP delivered/read cursors. Replay after reconnect uses subscribe.after; 410/sync.error requires HTTP history/state recovery. Ping/pong are WebSocket control frames, not JSON.", "$defs": definitions, "oneOf": [{"$ref": "#/$defs/" + name} for name in ("Subscribe", "Unsubscribe", "Event", "SubscriptionError", "SyncError")]}
    for name, value in (("openapi.json", spec), ("websocket.schema.json", ws)):
        (ROOT / "docs" / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    print(f"Generated {len(ROUTES)} source-covered HTTP routes and WebSocket JSON schemas")

if __name__ == "__main__": main()
