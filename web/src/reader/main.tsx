import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/shared/styles.css";
import { ReaderApp } from "./Body";

// biome-ignore lint/style/noNonNullAssertion: reader.html always has #root
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ReaderApp />
  </StrictMode>,
);
