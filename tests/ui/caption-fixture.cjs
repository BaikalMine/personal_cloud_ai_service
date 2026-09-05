async function installCaptionFixture(context, { autoComplete = false } = {}) {
  const state = { jobs: [], posts: [], actions: [], offline: false, autoComplete };
  let sequence = 0;
  await context.route("**/api/lora-datasets/*/captions", async (route) => {
    const request = route.request(); const datasetID = new URL(request.url()).pathname.split("/")[3];
    if (request.method() === "POST") {
      const body = request.postDataJSON(); state.posts.push(body);
      if (body.cancel) for (const job of state.jobs) { if (job.dataset_id === datasetID && ["queued", "running"].includes(job.state)) job.state = "cancelled"; }
      else {
        const view = await (await context.request.get(request.url().replace(/\/captions$/, ""))).json();
        for (const item of view.manifest.images) {
          if (!body.image_ids.includes(item.id) || item.excluded || (body.only_empty && item.caption.trim())) continue;
          state.jobs.push({ job_id: `fixture-job-${++sequence}`, dataset_id: datasetID, image_id: item.id, state: "queued", created_at: new Date(1800000000000 + sequence * 1000).toISOString(),
            source: { image: { ...item }, trigger_word: view.manifest.settings.trigger_word, concept_type: view.manifest.settings.concept_type }, caption: `${view.manifest.settings.trigger_word}, separately analyzed frame ${sequence}` });
        }
      }
    } else {
      if (state.offline) { await route.fulfill({ status: 504, json: { error: "Temporary upstream failure" } }); return; }
      if (state.autoComplete) for (const job of state.jobs) if (job.dataset_id === datasetID && job.state === "queued") job.state = "completed";
    }
    await route.fulfill({ status: request.method() === "POST" ? 202 : 200, json: { jobs: state.jobs.filter(job => job.dataset_id === datasetID) } });
  });
  await context.route("**/api/lora-training/caption/*/*", async (route) => {
    const parts = new URL(route.request().url()).pathname.split("/"); const action = parts.pop(); const jobID = parts.pop(); const job = state.jobs.find(job => job.job_id === jobID);
    state.actions.push({ id: job?.job_id, action });
    if (!job) { await route.fulfill({ status: 404, json: { error: "Missing job" } }); return; }
    job.state = action === "cancel" ? "cancelled" : "queued";
    await route.fulfill({ json: job });
  });
  state.refresh = (page) => page.evaluate(() => window.dispatchEvent(new Event("online")));
  return state;
}
module.exports = { installCaptionFixture };
