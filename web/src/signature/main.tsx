import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/shared/styles.css";
import { SignatureApp } from "./Signature";

// biome-ignore lint/style/noNonNullAssertion: signature.html always has #root
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <SignatureApp />
  </StrictMode>,
);
