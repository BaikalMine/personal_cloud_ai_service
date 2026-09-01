(function bootstrapDialogFocus(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayDialogFocus = api;
})(typeof window !== "undefined" ? window : null, function dialogFocusFactory() {
  const focusableSelector = [
    "a[href]",
    "button",
    "input",
    "select",
    "textarea",
    "[tabindex]",
  ].join(",");

  const focusableElements = (root) => {
    if (!root?.querySelectorAll) return [];
    return [...root.querySelectorAll(focusableSelector)].filter((element) => {
      if (element.disabled || element.hidden || element.tabIndex < 0) return false;
      if (element.getAttribute?.("aria-hidden") === "true") return false;
      return typeof element.getClientRects !== "function" || element.getClientRects().length > 0;
    });
  };

  const createFocusTrap = ({ root, documentRef } = {}) => {
    const documentObject = documentRef || (typeof document !== "undefined" ? document : null);
    let active = false;
    let returnFocus = null;
    let escapeAction = null;

    const focus = (element) => {
      if (!element?.focus) return false;
      element.focus({ preventScroll: true });
      return true;
    };

    const deactivate = ({ restore = true } = {}) => {
      if (active) documentObject?.removeEventListener?.("keydown", handleKeydown, true);
      active = false;
      escapeAction = null;
      const target = returnFocus;
      returnFocus = null;
      if (restore && target?.isConnected !== false) focus(target);
    };

    const handleKeydown = (event) => {
      if (!active || root?.hidden) return;
      if (event.key === "Escape" && escapeAction) {
        event.preventDefault();
        event.stopPropagation();
        escapeAction();
        return;
      }
      if (event.key !== "Tab") return;
      const elements = focusableElements(root);
      if (!elements.length) {
        event.preventDefault();
        return;
      }
      const currentIndex = elements.indexOf(documentObject?.activeElement);
      const nextIndex = event.shiftKey
        ? currentIndex <= 0 ? elements.length - 1 : currentIndex - 1
        : currentIndex < 0 || currentIndex === elements.length - 1 ? 0 : currentIndex + 1;
      event.preventDefault();
      focus(elements[nextIndex]);
    };

    const activate = ({ trigger, initialFocus, onEscape } = {}) => {
      if (!root || !documentObject) return;
      deactivate({ restore: false });
      returnFocus = trigger || documentObject.activeElement;
      escapeAction = typeof onEscape === "function" ? onEscape : null;
      active = true;
      documentObject.addEventListener?.("keydown", handleKeydown, true);
      focus(initialFocus) || focus(focusableElements(root)[0]);
    };

    return { activate, deactivate, isActive: () => active };
  };

  return { focusableElements, createFocusTrap };
});
