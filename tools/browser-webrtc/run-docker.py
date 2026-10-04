#!/usr/bin/env python3
"""Run genuine native Chromium in an owned, disposable Linux container."""
import pathlib, subprocess, uuid
root=pathlib.Path(__file__).resolve().parents[2]
name='qgramm-browser-'+uuid.uuid4().hex[:10]
image=name+':test'
try:
    subprocess.run(['docker','build','-f',str(root/'tools/browser-webrtc/Dockerfile'),'-t',image,str(root)],check=True)
    subprocess.run(['docker','run','--rm','--name',name,'--shm-size=256m',image],check=True)
finally:
    subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    subprocess.run(['docker','image','rm',image],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
