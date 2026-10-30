# Writes logits.bin beside windows.jsonl (TestFP32 writes it): each window's
# logits [s, 21] from the publisher's float32 model, model.onnx in the same
# folder, as float32 little-endian, in order:
#
#   uv run --no-project --python 3.13 --with onnxruntime==1.22.0 --with numpy \
#     python -I fp32_logits.py <the folder>
import json, sys, numpy as np, onnxruntime as ort
d = sys.argv[1]
s = ort.InferenceSession(d + "/model.onnx", providers=["CPUExecutionProvider"])
with open(d + "/windows.jsonl") as f, open(d + "/logits.bin", "wb") as out:
    for line in f:
        ids = np.array([json.loads(line)], dtype=np.int64)
        lg = s.run(["logits"], {"input_ids": ids, "attention_mask": np.ones_like(ids), "token_type_ids": np.zeros_like(ids)})[0][0]
        out.write(lg.astype("<f4").tobytes())
