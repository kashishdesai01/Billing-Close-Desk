import React from "react";
export function Badge({ state }: { state: string }) {
  return (
    <span className={"badge " + state}>
      <span />
      {state.replaceAll("_", " ")}
    </span>
  );
}
