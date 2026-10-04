// Measures native decoded PCM supplied by the remote MediaStreamAudioSourceNode.
class ReceiverPCM extends AudioWorkletProcessor {
  constructor(){super();this.samples=0;this.energy=0;this.blocks=0}
  process(inputs,outputs){const input=inputs[0]?.[0];if(input){this.samples+=input.length;for(const x of input)this.energy+=x*x;const output=outputs[0]?.[0];if(output)output.set(input)};if(++this.blocks%50===0)this.port.postMessage({samples:this.samples,energy:this.energy});return true}
}
registerProcessor('receiver-pcm',ReceiverPCM);
