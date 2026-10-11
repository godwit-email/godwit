import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/shared/styles.css";
import { WitPage } from "./WitPage";

// biome-ignore lint/style/noNonNullAssertion: wit.html always has #root
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <WitPage />
  </StrictMode>,
);
