// Профиль 07-languages, часть .NET.
//
// Тот же сценарий: activity -> таймер -> ожидание сигнала с таймаутом ->
// компенсация при отказе.
//
// Детерминизм здесь достигается собственными конструкциями SDK:
// Workflow.DelayAsync вместо Task.Delay, Workflow.WaitConditionAsync
// вместо самодельного ожидания, Workflow.UtcNow вместо DateTime.UtcNow.

using System.Reflection;
using Temporalio.Activities;
using Temporalio.Client;
using Temporalio.Worker;
using Temporalio.Workflows;

namespace TemporalCookbook;

public class Acts
{
    [Activity]
    public string Reserve(string resource)
    {
        Console.WriteLine($">>> ACTIVITY reserve({resource})");
        return $"res-{resource}-001";
    }

    [Activity]
    public string Allocate(string reservationId)
    {
        Console.WriteLine($">>> ACTIVITY allocate({reservationId})");
        return $"выделено {reservationId}";
    }

    [Activity]
    public string CancelReservation(string reservationId)
    {
        Console.WriteLine($">>> ACTIVITY cancelReservation({reservationId})");
        return $"снято {reservationId}";
    }
}

[Workflow]
public class CrossLangWorkflow
{
    private bool? approved;

    [WorkflowSignal("confirm")]
    public Task ConfirmAsync(bool value)
    {
        approved = value;
        return Task.CompletedTask;
    }

    [WorkflowRun]
    public async Task<string> RunAsync(string resource)
    {
        var opts = new ActivityOptions { StartToCloseTimeout = TimeSpan.FromSeconds(30) };

        var rid = await Workflow.ExecuteActivityAsync(
            (Acts a) => a.Reserve(resource), opts);

        // Детерминированный таймер SDK, а не Task.Delay.
        await Workflow.DelayAsync(TimeSpan.FromSeconds(2));

        // Возвращает false, если условие не наступило за отведённое время.
        var got = await Workflow.WaitConditionAsync(
            () => approved != null, TimeSpan.FromSeconds(60));

        if (got && approved == true)
        {
            await Workflow.ExecuteActivityAsync((Acts a) => a.Allocate(rid), opts);
            return "allocated";
        }

        await Workflow.ExecuteActivityAsync((Acts a) => a.CancelReservation(rid), opts);
        return "cancelled";
    }
}

public static class Program
{
    public static async Task Main(string[] args)
    {
        var address = Environment.GetEnvironmentVariable("TEMPORAL_ADDRESS")
                      ?? "temporal-frontend:7233";
        var queue = Environment.GetEnvironmentVariable("TASK_QUEUE") ?? "lang-dotnet-tq";
        // Версия берётся из загруженной сборки, а не из csproj.
        var sdk = typeof(TemporalClient).Assembly
                      .GetCustomAttribute<AssemblyInformationalVersionAttribute>()
                      ?.InformationalVersion ?? "неизвестна";

        var client = await TemporalClient.ConnectAsync(new(address));

        if (args.Length > 0 && args[0] == "run")
        {
            var approve = args.Contains("--approve");
            var handle = await client.StartWorkflowAsync(
                (CrossLangWorkflow wf) => wf.RunAsync("gpu-node-7"),
                new(id: "lang-dotnet", taskQueue: queue));

            await Task.Delay(4000);
            await handle.SignalAsync(wf => wf.ConfirmAsync(approve));

            var outcome = await handle.GetResultAsync();
            Console.WriteLine($"ЯЗЫК lang=dotnet sdk={sdk} queue={queue} outcome={outcome}");
            return;
        }

        using var worker = new TemporalWorker(
            client,
            new TemporalWorkerOptions(queue)
                .AddAllActivities(new Acts())
                .AddWorkflow<CrossLangWorkflow>());

        Console.WriteLine($"[worker] lang=dotnet sdk={sdk} queue={queue}");
        await worker.ExecuteAsync(CancellationToken.None);
    }
}
