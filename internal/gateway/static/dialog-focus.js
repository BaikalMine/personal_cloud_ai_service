(function bootstrapDialogFocus(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayDialogFocus = api;
})(typeof window !== "undefined" ? window : null, function dialogFocusFactory() {
  const stacksByDocument = new WeakMap();
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
    const stack = documentObject ? stacksByDocument.get(documentObject) || [] : [];
    if (documentObject) stacksByDocument.set(documentObject, stack);
    const registration = { root };
    let active = false;
    let returnFocus = null;
    let escapeAction = null;

    const focus = (element) => {
      if (!element?.focus) return false;
      element.focus({ preventScroll: true });
      return true;
    };

    const deactivate = ({ restore = true } = {}) => {
      const position = stack.indexOf(registration);
      const wasTop = position >= 0 && position === stack.length - 1;
      if (position >= 0) stack.splice(position, 1);
      if (active) documentObject?.removeEventListener?.("keydown", handleKeydown, true);
      active = false;
      escapeAction = null;
      const target = returnFocus;
      returnFocus = null;
      if (restore && wasTop && target?.isConnected !== false) focus(target);
    };

    const handleKeydown = (event) => {
      if (!active || event.defaultPrevented) return;
      // Lower dialogs keep their state but cannot handle the upper dialog's keys.
      const top = stack.findLast(({ root: surface }) => !surface.hidden && surface.isConnected !== false
        && (surface.tagName !== "DIALOG" || surface.open));
      if (top !== registration) return;
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
      stack.push(registration);
      documentObject.addEventListener?.("keydown", handleKeydown, true);
      focus(initialFocus) || focus(focusableElements(root)[0]);
    };

    return { activate, deactivate, isActive: () => active };
  };

  return { focusableElements, createFocusTrap };
});
