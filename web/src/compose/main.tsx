import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/shared/styles.css";
import { ComposePage } from "./ComposePage";

// biome-ignore lint/style/noNonNullAssertion: compose.html always has #root
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ComposePage />
  </StrictMode>,
);
