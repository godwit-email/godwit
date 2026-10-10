import { render, screen } from "@testing-library/react";
import { ComposePage } from "./ComposePage";

test("renders the compose page shell", () => {
  render(<ComposePage />);
  expect(screen.getByRole("main", { name: "撰写" })).toBeInTheDocument();
});
